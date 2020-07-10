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

// ------------------------------ snapshot ------------------------------

// Snapshot 返回快照
func (l *Log) Snapshot() (pb.Snapshot, error) {
	if l.pendingSnapshot != nil {
		return *l.pendingSnapshot, nil
	}
	return l.storage.Snapshot()
}

// Restore 恢复
func (l *Log) Restore(s pb.Snapshot) {
	log.Printf("log [%+v] starts to restore snapshot [index: %d, term: %d]\n", l, s.Metadata.Index, s.Metadata.Term)
	l.committed = s.Metadata.Index
	l.entries = nil
	l.stabled = s.Metadata.Index
	l.offset = s.Metadata.Index + 1
	l.snapIndex = s.Metadata.Index
	l.snapTerm = s.Metadata.Term
	l.pendingSnapshot = &s
}

// ------------------------------ normal interface ------------------------------

// FirstIndex front
func (l *Log) FirstIndex() uint64 {
	if len(l.entries) != 0 {
		return l.entries[0].Index
	} else if l.pendingSnapshot != nil {
		return l.pendingSnapshot.Metadata.Index
	}
	return l.snapIndex
}

// LastIndex end
func (l *Log) LastIndex() uint64 {
	if len(l.entries) != 0 {
		return l.entries[len(l.entries)-1].Index
	} else if l.pendingSnapshot != nil {
		return l.pendingSnapshot.Metadata.Index
	}
	return l.snapIndex
}

// LastTerm return the last term
func (l *Log) LastTerm() uint64 {
	t, err := l.Term(l.LastIndex())
	if err != nil {
		panicf("unexpected error when getting the last term (%v)", err)
	}
	return t
}

// IsUpToDate check status
func (l *Log) IsUpToDate(lasti, term uint64) bool {
	return term > l.LastTerm() || (term == l.LastTerm() && lasti >= l.LastIndex())
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
func (l *Log) matchTermWithInf(idx, term uint64) (uint64, bool, error) {
	idxt, err := l.Term(idx)
	return idxt, err == nil && idxt == term, err
}

// ------------------------------ append ------------------------------

// golang 这么写真不缺时空...
// 这里的检查是输入ents和log中已有数据(且在snap阶段或entries中)的检查
func (l *Log) findConflict(ents []pb.Entry) uint64 {
	for _, ent := range ents {
		if idxt, ok, err := l.matchTermWithInf(ent.Index, ent.Term); !ok {
			if ent.Index <= l.LastIndex() {
				log.Printf("found conflict at index %d [existing term: %d, conflicting term: %d]\n",
					ent.Index, zeroTermOnRangeErr(idxt, err), ent.Term)
			}
			return ent.Index
		}
	}
	return 0
}

// ents 表示已经被拷贝了无数次了woc
func (l *Log) truncateAppend(ents []pb.Entry) {
	after := ents[0].Index
	if after == l.LastIndex()+1 {
		// 要追加的记录正好连续
		l.entries = append(l.entries, ents...)
		return
	}
	log.Printf("truncate the unstable entries before index %d\n", after)
	if after-1 < l.stabled {
		l.stabled = after - 1
	}
	// 把原有的entries删除后半段 再追加
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

// Append 直接追加
func (l *Log) Append(ents ...pb.Entry) uint64 {
	if len(ents) == 0 {
		return l.LastIndex()
	}
	if after := ents[0].Index - 1; after < l.committed {
		// 醉了
		panicf("after(%d) is out of range [committed(%d)]", after, l.committed)
	}
	// ...
	l.truncateAppend(ents)
	l.tryCompact()
	return l.LastIndex()
}

// TryAppend 入参更多 场景：
// ents在etcd的实现中 顶层使用的也是数组 这里直接改数组保持代码一致且无歧义==> 改...方便压入单项
func (l *Log) TryAppend(index, logTerm, committed uint64, ents ...pb.Entry) (newlast uint64, ok bool) {
	// index logTerm为leader上次发送给该follower的日志索引和term
	// committed 是可以提交的日志索引 只有follower能匹配这个才能响应
	if l.MatchTerm(index, logTerm) {
		// 按照ents计算的最新日志索引 找到ents内不一致的位置
		newlast = index + uint64(len(ents))
		cfIdx := l.findConflict(ents)
		switch {
		case cfIdx == 0:
		case cfIdx <= l.committed:
			panicf("entry %d conflict with committed entry [committed(%d)]", cfIdx, l.committed)
		default:
			// 取出从冲突位置开始的日志 覆盖自己的日志
			offset := index + 1
			l.Append(ents[cfIdx-offset:]...)
		}
		// 如果leader的committed日志大于leader复制给当前follower的最新日志索引 committed > newlast
		// 说明follower落后 直接全部提交    否则提交leader已经提交的索引的日志 即committed
		l.CommitTo(min(committed, newlast))
		return newlast, true
	}
	return 0, false
}

// ------------------------------ get entries ------------------------------

// TODO Slice调用这里有点冗余 后期做好错误处理后合并这块代码
// l.firstIndex <= lo <= hi <= l.firstIndex + len(l.entries)
func (l *Log) mustCheckOutOfBounds(lo, hi uint64) error {
	if lo > hi {
		log.Panicf("invalid slice %d > %d\n", lo, hi)
	}
	fi := l.FirstIndex()
	if lo < fi {
		return errCompacted
	}
	if hi > l.LastIndex()+1 {
		panicf("slice[%d,%d) out of bound [%d,%d]\n", lo, hi, fi, l.LastIndex())
	}
	return nil
}

// Slice get ents from lo to hi
func (l *Log) Slice(lo, hi uint64) ([]pb.Entry, error) {
	if err := l.mustCheckOutOfBounds(lo, hi); err != nil || len(l.entries) == 0 {
		return nil, err
	}
	return l.entries[lo-l.offset : hi-l.offset], nil
}

func (l *Log) hasSliceSince(since uint64) bool {
	off := max(since+1, l.FirstIndex())
	return l.committed+1 > off
}

func (l *Log) sliceSince(since uint64) []pb.Entry {
	off := max(since+1, l.FirstIndex())
	if l.committed+1 > off {
		ents, err := l.Slice(off, l.committed+1)
		if err != nil {
			panicf("unexpected error when getting unapplied entries (%v)\n", err)
		}
		return ents
	}
	return nil
}

// HasSliceNotApplied check the entries
func (l *Log) HasSliceNotApplied() bool {
	return l.hasSliceSince(l.applied)
}

// SliceNotApplied returns all the [committed but not applied] entries
func (l *Log) SliceNotApplied() (ents []pb.Entry) {
	return l.sliceSince(l.applied)
}

// UnstableEntries returun unstable ents
func (l *Log) UnstableEntries() []pb.Entry {
	if int(l.stabled+1-l.offset) > len(l.entries) {
		return nil
	}
	return l.entries[l.stabled+1-l.offset:]
}

func (l *Log) allEntries() []pb.Entry {
	return l.entries
}

// Entries return ents since idx
func (l *Log) Entries(idx uint64) ([]pb.Entry, error) {
	if idx < l.FirstIndex() {
		return nil, errCompacted
	} else if idx > l.LastIndex() {
		return nil, nil
	}
	return l.entries[idx-l.offset:], nil
}

// ------------------------------ interface To ------------------------------

// CommitTo ...
func (l *Log) CommitTo(tocommit uint64) {
	if l.committed < tocommit {
		if l.LastIndex() < tocommit {
			panicf("tocommit(%d) is out of range [lastIndex(%d)]. Was the raft log corrupted, truncated, or lost?", tocommit, l.LastIndex())
		}
		l.committed = tocommit
	}
}

// TryCommit ...
func (l *Log) TryCommit(maxIndex, term uint64) bool {
	if maxIndex > l.committed && l.MatchTerm(maxIndex, term) {
		l.CommitTo(maxIndex)
		return true
	}
	return false
}

// AppliedTo ...
func (l *Log) AppliedTo(idx uint64) {
	if idx == 0 {
		return
	} else if l.committed < idx || idx < l.applied {
		panicf("applied(%d) is out of range [prevApplied(%d), committed(%d)]\n", idx, l.applied, l.committed)
	}
	l.applied = idx
}

// StableTo ...
func (l *Log) StableTo(idx, term uint64) {
	if l.MatchTerm(idx, term) && l.stabled < idx {
		l.stabled = idx
	}
}

// StableSnapTo ...
func (l *Log) StableSnapTo(idx uint64) {
	if l.pendingSnapshot != nil && l.pendingSnapshot.Metadata.Index == idx {
		l.pendingSnapshot = nil
	}
}
