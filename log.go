package raft

import (
	"log"

	pb "github.com/binacsgo/raft/eraftpb"
)

// Log 管理日志条目, 结构形如:
//
//  snapshot/first.....applied....committed....stabled.....last
//  --------|------------------------------------------------|
//                            log entries
// 			entries 无阶段划分
type Log struct {
	// 落盘数据
	storage Storage

	// 已知已落盘的最高数据idx
	committed uint64

	// 程序应用于状态机的最高位置
	// 恒等: applied <= committed
	applied uint64

	// 未持久化的日志 index <= stabled 最终都会存储
	// Everytime handling `Ready`, the unstabled logs will be included.
	stabled uint64

	// all entries that have not yet compact.
	entries []pb.Entry

	// Data.
	offset    uint64 // logic index - offset = slice index
	snapTerm  uint64
	snapIndex uint64
	// the incoming unstable snapshot, if any.
	pendingSnapshot *pb.Snapshot
}

// 依据 storage 恢复 RaftLog
func newLog(storage Storage) *Log {
	if storage == nil {
		panic("ILLEGAL: storage is nil")
	}
	firstIdx, err := storage.FirstIndex()
	if err != nil {
		panic(err)
	}
	lastIdx, err := storage.LastIndex()
	if err != nil {
		panic(err)
	}
	snapTerm, err := storage.Term(firstIdx - 1)
	if err != nil {
		panic(err)
	}
	entries, err := storage.Entries(firstIdx, lastIdx+1)
	if err != nil {
		panic(err)
	}
	ret := &Log{
		storage:   storage,
		committed: firstIdx - 1,
		applied:   firstIdx - 1,
		stabled:   lastIdx,
		entries:   entries,
		offset:    firstIdx,
		snapTerm:  snapTerm,
		snapIndex: firstIdx - 1,
	}
	return ret
}

// ------------------------------ term ------------------------------

// Term return the term of the entry in the given index
func (l *Log) Term(idx uint64) (uint64, error) {
	if idx == l.snapIndex {
		return l.snapTerm, nil
	} else if len(l.entries) == 0 || idx < l.offset {
		return 0, errCompacted
	} else if idx > l.offset+uint64(len(l.entries))-1 {
		return 0, errUnavailable
	}
	return l.entries[idx-l.offset].Term, nil
}

// MatchTerm match the `idx's term` with the `term`
func (l *Log) MatchTerm(idx, term uint64) bool {
	idxt, err := l.Term(idx)
	return err == nil && idxt == term
}

// MatchTermWithInf match the `idx's term` with the `term`
// Write the return elegantly
func (l *Log) MatchTermWithInf(idx, term uint64) (uint64, bool, error) {
	idxt, err := l.Term(idx)
	return idxt, err == nil && idxt == term, err
}

// ------------------------------ append ------------------------------

// golang 这么写真不缺时空...
// 这里底层执行两次 Term 可以优化 ==> finished
// 这里的检查是输入ents和log中已有数据(且在snap阶段或entries中)的检查
func (l *Log) findConflict(ents []pb.Entry) uint64 {
	for _, ent := range ents {
		if idxt, ok, err := l.MatchTermWithInf(ent.Index, ent.Term); !ok {
			if ent.Index <= l.lastIndex() {
				log.Printf("found conflict at index %d [existing term: %d, conflicting term: %d]\n",
					ent.Index, zeroTermOnRangeErr(idxt, err), ent.Term)
			}
			// 这里返回啥继续思考
			return ent.Index
		}
	}
	return 0
}

// ents 表示已经被拷贝了无数次了woc
func (l *Log) truncateAppend(ents []pb.Entry) {
	after := ents[0].Index
	if after == l.lastIndex()+1 {
		l.entries = append(l.entries, ents...)
		return
	}
	log.Printf("truncate the unstable entries before index %d\n", after)
	if after-1 < l.stabled {
		l.stabled = after - 1
	}
	// ...
	l.entries = append([]pb.Entry{}, l.entries[:after-l.offset]...)
	l.entries = append(l.entries, ents...)
}

// 压缩日志 落盘
func (l *Log) tryCompact() {
	var s, t uint64
	var err error
	for {
		s, err = l.storage.FirstIndex()
		if err != nil {
			panic(err)
		}
		t, err = l.storage.Term(s - 1)
		if err == errCompacted {
			if i, _ := l.storage.FirstIndex(); i != s {
				// 函数内获取s之后进行过压缩了 所以重试
				continue
			}
		} else if err != nil {
			panic(err)
		}
		break
	}
	compactSize := s - l.offset
	if compactSize > 0 && compactSize < uint64(len(l.entries)) {
		l.entries = l.entries[compactSize:]
		l.offset = s
		l.snapIndex = s - 1
		l.snapTerm = t
	}
}

func (l *Log) append(ents []pb.Entry) uint64 {
	// 重复逻辑少一些八...
	if len(ents) == 0 {
		return l.lastIndex()
	}
	if after := ents[0].Index - 1; after < l.committed {
		// 醉了
		panicf("after(%d) is out of range [committed(%d)]", after, l.committed)
	}
	// ...
	l.truncateAppend(ents)
	l.tryCompact()
	return l.lastIndex()
}

// tryAppend
// ents在etcd的实现中 顶层使用的也是数组 这里直接改数组保持代码一致且无歧义
// 直接使用pb数据结构的话对其校验的实现会很丑很丑... TODO
func (l *Log) tryAppend(index, logTerm, committed uint64, ents []pb.Entry) (newlast uint64, ok bool) {
	if l.MatchTerm(index, logTerm) {
		newlast = index + uint64(len(ents))
		cf := l.findConflict(ents)
		switch {
		case cf == 0:
		case cf <= l.committed:
			// 这不是重复比较了吗？？？？
			panicf("entry %d conflict with committed entry [committed(%d)]", cf, l.committed)
		default:
			// 这switch用的... 正常逻辑都放在default里了woc
			offset := index + 1
			l.append(ents[cf-offset:])
		}

	}
	return 0, false
}

// ------------------------------ snapshot ------------------------------
func (l *Log) snapshot() (pb.Snapshot, error) {
	if l.pendingSnapshot != nil {
		return *l.pendingSnapshot, nil
	}
	return l.storage.Snapshot()
}

func (l *Log) restore(s pb.Snapshot) {
	log.Printf("log [%+v] starts to restore snapshot [index: %d, term: %d]\n", l, s.Metadata.Index, s.Metadata.Term)
	l.committed = s.Metadata.Index
	l.entries = nil
	l.stabled = s.Metadata.Index
	l.offset = s.Metadata.Index + 1
	l.snapIndex = s.Metadata.Index
	l.snapTerm = s.Metadata.Term
	l.pendingSnapshot = &s
}

// ------------------------------ nextEnts ------------------------------
// 函数名改一下吧... 这命名真是醉了
func (l *Log) firstIndex() uint64 {
	if len(l.entries) != 0 {
		return l.entries[0].Index
	} else if l.pendingSnapshot != nil {
		return l.pendingSnapshot.Metadata.Index
	}
	return l.snapIndex
}

func (l *Log) lastIndex() uint64 {
	if len(l.entries) != 0 {
		return l.entries[len(l.entries)-1].Index
	} else if l.pendingSnapshot != nil {
		// 为啥也用的Metadata.Index
		return l.pendingSnapshot.Metadata.Index
	}
	return l.snapIndex
}

// l.firstIndex <= lo <= hi <= l.firstIndex + len(l.entries)
func (l *Log) mustCheckOutOfBounds(lo, hi uint64) error {
	if lo > hi {
		log.Panicf("invalid slice %d > %d", lo, hi)
	}
	fi := l.firstIndex()
	if lo < fi {
		return errCompacted
	}

	if hi > l.lastIndex()+1 {
		panicf("slice[%d,%d) out of bound [%d,%d]", lo, hi, fi, l.lastIndex())
	}
	return nil
}

func (l *Log) slice(lo, hi uint64) ([]pb.Entry, error) {
	if err := l.mustCheckOutOfBounds(lo, hi); err != nil || len(l.entries) == 0 {
		return nil, err
	}
	return l.entries[lo-l.offset : hi-l.offset], nil
}

func (l *Log) nextEntsSince(since uint64) []pb.Entry {
	off := max(since+1, l.firstIndex())
	if l.committed+1 > off {
		ents, err := l.slice(off, l.committed+1)
		if err != nil {
			panicf("unexpected error when getting unapplied entries (%v)\n", err)
		}
		return ents
	}
	return nil
}

// nextEnts returns all the [committed but not applied] entries
// 用于：
func (l *Log) nextEnts() (ents []pb.Entry) {
	return l.nextEntsSince(l.applied)
}

func (l *Log) hasNextEntsSince(since uint64) bool {
	off := max(since+1, l.firstIndex())
	return l.committed+1 > off
}

func (l *Log) hasNextEnts() bool {
	return l.hasNextEntsSince(l.applied)
}

// ------------------------------ upToDate ------------------------------
func (l *Log) lastTerm() uint64 {
	t, err := l.Term(l.lastIndex())
	if err != nil {
		panicf("unexpected error when getting the last term (%v)", err)
	}
	return t
}

func (l *Log) isUpToDate(lasti, term uint64) bool {
	return term > l.lastTerm() || (term == l.lastTerm() && lasti >= l.lastIndex())
}

// ------------------------------ entries ------------------------------
func (l *Log) unstableEntries() []pb.Entry {
	if int(l.stabled+1-l.offset) > len(l.entries) {
		return nil
	}
	return l.entries[l.stabled+1-l.offset:]
}

func (l *Log) allEntries() []pb.Entry {
	return l.entries
}

// Entries ...
func (l *Log) Entries(i uint64) ([]pb.Entry, error) {
	if i < l.firstIndex() {
		return nil, errCompacted
	}
	if i > l.lastIndex() {
		return nil, nil
	}
	return l.entries[i-l.offset:], nil
}

// ------------------------------ interface To ------------------------------
func (l *Log) commitTo(tocommit uint64) {
	if l.committed < tocommit {
		if l.lastIndex() < tocommit {
			panicf("tocommit(%d) is out of range [lastIndex(%d)]. Was the raft log corrupted, truncated, or lost?", tocommit, l.lastIndex())
		}
		l.committed = tocommit
	}
}

func (l *Log) appliedTo(i uint64) {
	if i == 0 {
		return
	}
	if l.committed < i || i < l.applied {
		panicf("applied(%d) is out of range [prevApplied(%d), committed(%d)]\n", i, l.applied, l.committed)
	}
	l.applied = i
}

func (l *Log) stableTo(idx, term uint64) {
	if l.MatchTerm(idx, term) && l.stabled < idx {
		l.stabled = idx
	}
}

func (l *Log) stableSnapTo(i uint64) {
	if l.pendingSnapshot != nil && l.pendingSnapshot.Metadata.Index == i {
		l.pendingSnapshot = nil
	}
}

//
func (l *Log) maybeCommit(maxIndex, term uint64) bool {
	if maxIndex > l.committed && l.MatchTerm(maxIndex, term) {
		l.commitTo(maxIndex)
		return true
	}
	return false
}
