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



### 2.2 方法实现