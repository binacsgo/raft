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
    storage Storage     // 已落盘的最高数据
    committed uint64    // 节点状态机的最高位置 
    applied uint64      // 恒等: applied <= committed
    stabled uint64      // 未持久化的日志 index <= stabled 最终都会存储

    entries []pb.Entry

    // Data.
    offset uint64    // logic index - offset = slice index
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
// --------- 在文件内被调用的函数 ---------
findConflict
truncateAppend
tryCompact
mustCheckOutOfBounds
nextEntsSince
hasNextEntsSince 更名sliceSince
// --------- 在本包内被调用的函数 ---------
newLog                显然... raft.go
Term                  文件内 以及 raft.go            
matchTerm             文件内 以及 raft.go    大写 修改返回值：idx对应的term和比较bool
zeroTermOnRangeErr    文件内 以及 raft.go    改成非成员方法放在util
append                文件内 以及 raft.go
tryAppend             文件内 以及 raft.go
snapshot              文件内 以及 raft.go
restore               文件内 以及 raft.go
firstIndex            文件内 以及 raft.go
lastIndex             文件内 以及 raft.go 以及app层的 peer.go
slice                 文件内 以及 raft.go
lastTerm              文件内 以及 raft.go
isUpToDate            文件内 以及 raft.go
unstableEntries       文件内 以及 rawnode.go
allEntries            文件内 以及 rawnode.go 以及 util输出用
hasNextEnts           文件内 以及 rawnode.go
nextEnts              文件内 以及 rawnode.go
Entries               只看到 raft.go
commitTo              raft.go
maybeCommit           raft.go
appliedTo             raft.go    rawnode.go
stableTo              rawnode.go
stableSnapTo          rawnode.go
```

重构如下：

```go
// --------- inner ---------
matchTermWithInf      返回匹配前下标和错误码
findConflict    
truncateAppend
tryCompact
mustCheckOutOfBounds  Slice使用
hasSliceSince
sliceSince
allEntries            只有打日志用

// --------- package ---------
newLog              raft.go
Snapshot            raft.go/sendAppend
Restore             raft.go/restore

FirstIndex          raft.go/sendAppend 打日志用
LastIndex           raft.go/newRaft｜reset｜appendEntry｜becomeLeader｜campaign｜Step
                           ｜stepLeader｜handleSnapshot｜restore｜restoreNode｜addNode
                           ｜loadState
                    以及app层的 peer.go
LastTerm            raft.go/newRaft｜campaign｜Step｜restore
isUpToDate          raft.go/Step

Term                raft.go/sendAppend
MatchTerm           raft.go/restore
Append              raft.go/appendEntry
TryAppend           raft.go/handleAppendEntries
Slice               raft.go/Step
HasSliceNotApplied  rawnode.go/HasReady
SliceNotApplied     rawnode.go/newReady
unstableEntries     rawnode.go/newReady｜HasReady
Entries             raft.go/sendAppend

CommitTo            raft.go/handleHeartbea｜restore
TryCommit           raft.go/maybeCommit
AppliedTo           raft.go/newRaft rawnode.go/Advance
StableTo            rawnode.go/Advance
StableSnapTo        rawnode.go/Advance
// --------- app ---------
主要是在 raftstore层调用 r.RaftLog.LastIndex() 
被app调用的函数可以在raft结构封装一层 避免访问raftLog然后再调用其方法造成的混乱
    所以做以下约定：
1. log 文件内部使用的函数小写开头
2. log 包内部使用的函数大写开头
3. raftLog 改为 raft 的私有成员 避免app直接调用raftLog的大写函数

// --------- util ---------
zeroTermOnRangeErr
```



## 3. raft

