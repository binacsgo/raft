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

<!--Tools:go get -u github.com/ofabry/go-callvis
brew install graphviz-->

整个包的接口都是 rawnode 的方法



**Unit Test**

```shell
go test -v -coverprofile cover.out
go tool cover -html=cover.out -o cover.html 

# paper_test
go test -v raft_paper_test.go error.go storage.go util.go raft.go rawnode.go log.go raft_test.go
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

### 3.1 定义

> 前面是
>
> 1. config 及其校验 validate
> 2. 全局随机数生成器
> 3. 对于 Leader 保存从节点进度的 Progress

重构结构：

```
// --------- 在文件内被调用的函数 ---------
send                消息加入msgs
sendTimeoutNow      发送timeoutNow 在stepLeader中使用
sendAppend          发送附带日志的消息 根据从log里能否拿到日志决定发送快照还是日志段
sendHeartbeat       发送心跳
bcastAppend         广播 向所有节点sendAppend 
bcastHeartbeat      广播心跳
tryCommit           校验日志是否可以提交 复制到过半的节点就可以提交 调用log的TryCommit
appendEntry         

tickElection        选举超时后follower和candidate会执行
tickHeartbeat       心跳超时后leader发送心跳MsgBeat
            
becomeFollower      更新节点状态
becomeCandidate     
becomeLeader        更新节点状态 称为leader会出发appendEntry 但传入空ent

campaign            竞选 -成功会成为-这里直接成为候选人candidate状态 如果投票足够变leader 发起投票请求

handleAppendEntries 处理追加日志appendEntries
handleHeartbeat     处理心跳 更新raftLog中的Commit并发送心跳应答
handleSnapshot      

stepLeader
stepCandidate
stepFollower

... 一些小的工具类函数

// --------- 在本包内被调用的函数 ---------
Step                rawnode.go
GetSnap             
SoftState           
HardState           
GetProgress         
Tick                
AddNode             rawnode.go
RemoveNode          rawnode.go

```

### 3.2 状态机

回顾之前定义的消息：

```go
const (
	// 选举超时 指示本节点进入选举
	MessageType_MsgHup MessageType = 0
	// 成为 Leader 指示本节点向followers发送 MsgHeartbeat
	MessageType_MsgBeat MessageType = 1
	// 提议向 Leader 的日志条目中添加信息
	MessageType_MsgPropose MessageType = 2
	// 包含要复制的日志条目
	MessageType_MsgAppend MessageType = 3
	// 回应append
	MessageType_MsgAppendResponse MessageType = 4
	// 请求选举投票
	MessageType_MsgRequestVote MessageType = 5
	// 回应选举投票
	MessageType_MsgRequestVoteResponse MessageType = 6
	// TODO 请求安装快照消息
	MessageType_MsgSnapshot MessageType = 7
	// Leader 向 followers 发送的心跳
	MessageType_MsgHeartbeat MessageType = 8
	// 心跳回应
	MessageType_MsgHeartbeatResponse MessageType = 9
	// TODO 要求 目标Leader 下台
	MessageType_MsgTransferLeader MessageType = 11
	// Leader 向别的目标Leader发送此消息要求对方立即下台
	MessageType_MsgTimeoutNow MessageType = 12
)
```

**在 Step 中收到消息 m**

1. 校验 m.Term 对于 m.Term > r.Term 的情况（其他情况的处理不影响本节点状态转移故此处略去）：

   若消息类型为 `append`  `heartbeat`  `snapshot`  则执行 becomeFollower( , from)

   否则执行 becomeFollower( , None)

2. 校验 m.Type

   hup：如果不是 leader 则参与竞选；否则已是 leader 保持不动

   vote：投赞成 or 反对

   其他：依据当前节点角色处理消息

**对于非 hup/vote 的消息类型，节点依据不同角色执行以下流程**

<!-- https://tableconvert.com -->

| 消息 \ 角色         | Leader                         | Candidate | Follower |
| :----------------: | :----------------------------: | :-------: | :------: |
| Beat                | 广播心跳                       | -         | - |
| Propose             | 广播日志                       | err | err |
| Append              | -                              | 成为follower 随即handle日志 | handle日志 |
| AppendResponse      | 拒绝==> Decr \| 接受==> Update | - | - |
| Heartbeat           | -                              | 成为follower 随即handle心跳 | handle心跳 |
| HeartbeatResponse   | send                           | - | - |
| RequestVoteResponse | -                              | 处理投票 并判断是否能成为leader 再处理 | - |
| Snapshot            | -                              | - | handle快照 |
| TransferLeader      | 转移                           | - | 转移 |
| TimeoutNow          | -                              | 忽略 | 判断 进入竞选 |



## 4. rawnode

```go
Tick
Campaign
Propose
ProposeConfChange
ApplyConfChange
Step
GetSnap
Ready
HasReady
Advance
GetProgress
TransferLeader
```

