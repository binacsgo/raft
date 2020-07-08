package raft

import pb "github.com/binacsgo/raft/eraftpb"

// RaftLog 管理日志条目, 结构形如:
//
//  snapshot/first.....applied....committed....stabled.....last
//  --------|------------------------------------------------|
//                            log entries
// 			无截断
type RaftLog struct {
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
func newLog(storage Storage) *RaftLog {
	log := &RaftLog{
		storage: storage,
	}

	return log
}

// 压缩日志 落盘
func (l *RaftLog) maybeCompact() {
}

// 返回列表
func (l *RaftLog) unstableEntries() []pb.Entry {
	return nil
}

// nextEnts returns all the [committed but not applied] entries
func (l *RaftLog) nextEnts() (ents []pb.Entry) {
	return nil
}

// LastIndex return the last index of the log entries
func (l *RaftLog) LastIndex() uint64 {
	return 0
}

// Term return the term of the entry in the given index
func (l *RaftLog) Term(i uint64) (uint64, error) {
	return 0, nil
}
