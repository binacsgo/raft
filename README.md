# raft

Raft core implement.



> [raft 论文-中文](https://github.com/maemual/raft-zh_cn/blob/master/raft-zh_cn.md)



## 0. overview

```markdown
# storage

# log

# raft

# rawnode
```



## 1. storage

存储接口

### 1.1 接口定义

```go
type Storage interface {
  // 恢复storage
	InitialState() (pb.HardState, pb.ConfState, error)
  // 获取entries
	Entries(lo, hi uint64) ([]pb.Entry, error)
	// 返回entry[i]对应的term
	Term(i uint64) (uint64, error)
	LastIndex() (uint64, error)
	FirstIndex() (uint64, error)
  // 返回最近的snapshot
	Snapshot() (pb.Snapshot, error)
}
```



### 1.2 接口实现

`MemoryStorage` 内存



## 2. log

数据日志

### 2.1 结构定义

```go
//  snapshot/first.....applied....committed....stabled.....last
//  --------|------------------------------------------------|
//                            log entries
// 
type RaftLog struct {
	storage Storage
	// 已落盘的最高数据
	committed uint64
	// 节点状态机的最高位置 
	// 恒等: applied <= committed
	applied uint64
	// 未持久化的日志 index <= stabled 最终都会存储
	stabled uint64

	entries []pb.Entry

	// Data.
	offset uint64	// logic index - offset = slice index
	snapTerm uint64
	snapIndex uint64
	// the incoming unstable snapshot, if any.
	pendingSnapshot *pb.Snapshot
}
```

>   在 etcd 的 raft 实现中，对 entries 按阶段做了切分，这里简化实现直接使用 uint64 的下标指示各个阶段的 entries 。



### 2.2 实现

原共有以下函数：

```go
// term
Term(i uint64) (uint64, error)
// append
matchTerm(idx, term uint64) bool
zeroTermOnRangeErr(term uint64, err error) uint64
findConflict(ents []pb.Entry) uint64
truncateAppend(ents []pb.Entry)
append(ents []pb.Entry) uint64
tryCompact()
tryAppend(index, logTerm, committed uint64, ents []pb.Entry) (newlast uint64, ok bool)
// snapshot
snapshot() (pb.Snapshot, error)
restore(s pb.Snapshot)
// nextEnts
firstIndex() uint64
lastIndex() uint64
mustCheckOutOfBounds(lo, hi uint64) error
slice(lo, hi uint64) ([]pb.Entry, error)
nextEntsSince(since uint64)
nextEnts() (ents []pb.Entry)
hasNextEntsSince(since uint64) bool
hasNextEnts() bool
// upToDate
lastTerm() uint64
isUpToDate(lasti, term uint64) bool
// entries
unstableEntries() []pb.Entry
allEntries() []pb.Entry
Entries(i uint64) ([]pb.Entry, error)
// to interface
commitTo(tocommit uint64)
appliedTo(i uint64)
stableTo(idx, term uint64)
stableSnapTo(i uint64)
maybeCommit(maxIndex, term uint64) bool

// --------- 在本包内被调用的函数 ---------
newLog							显然... raft.go
Term								文件内 以及 raft.go			
matchTerm						文件内 以及 raft.go			大写 修改返回值：idx对应的term和比较bool
zeroTermOnRangeErr	文件内 以及 raft.go			改成非成员方法放在util
findConflict				文件内	
truncateAppend			文件内
tryCompact					文件内
append							文件内 以及 raft.go
tryAppend						文件内 以及 raft.go
snapshot						文件内 以及 raft.go
restore							文件内 以及 raft.go
firstIndex					文件内 以及 raft.go
lastIndex						文件内 以及 raft.go 以及app层的 peer.go
mustCheckOutOfBounds	文件内
slice								文件内 以及 raft.go
nextEntsSince				文件内
nextEnts						文件内 以及 rawnode.go
hasNextEntsSince		文件内
hasNextEnts					文件内 以及 rawnode.go
lastTerm						文件内 以及 raft.go
isUpToDate					文件内 以及 raft.go
unstableEntries			文件内 以及 rawnode.go
allEntries					文件内 以及 rawnode.go 以及 util
Entries							只看到 raft.go
commitTo						raft.go
maybeCommit					raft.go
appliedTo						raft.go	rawnode.go
stableTo						rawnode.go
stableSnapTo				rawnode.go
```

