package raft

import (
	"fmt"
	"log"
	"math/rand"
	"sort"
	"sync"
	"time"

	pb "github.com/binacsgo/raft/eraftpb"
)

// ------------------------------ node statue ------------------------------

// None is a placeholder node ID used when there is no leader.
const None uint64 = 0

const (
	// StateFollower ...
	StateFollower StateType = iota
	// StateCandidate ...
	StateCandidate
	// StateLeader ...
	StateLeader
)

// StateType represents the role of a node in a cluster.
type StateType uint64

var stmap = [...]string{
	"StateFollower",
	"StateCandidate",
	"StateLeader",
}

func (st StateType) String() string {
	return stmap[uint64(st)]
}

// ------------------------------ config ------------------------------

// Config of raft
type Config struct {
	ID    uint64 // cannot be 0.
	peers []uint64

	ElectionTick  int
	HeartbeatTick int

	Storage Storage
	Applied uint64 // the last applied index.
}

func (c *Config) validate() error {
	if c.ID == None {
		return errConfigValidateIDisNone
	}
	if c.HeartbeatTick <= 0 {
		return errConfigValidateHeartbeatTick
	}
	if c.ElectionTick <= c.HeartbeatTick {
		return errConfigValidateElectionTick
	}
	if c.Storage == nil {
		return errConfigValidateStorageNil
	}
	return nil
}

// ------------------------------ rand ------------------------------
type lockedRand struct {
	mu   sync.Mutex
	rand *rand.Rand
}

func (r *lockedRand) Intn(n int) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	v := r.rand.Intn(n)
	return v
}

var globalRand = &lockedRand{
	rand: rand.New(rand.NewSource(time.Now().UnixNano())),
}

// ------------------------------ Progress ------------------------------

// Progress follower’s progress in the view of the leader
type Progress struct {
	Match, Next uint64
}

func (pr *Progress) maybeUpdate(n uint64) bool {
	var updated bool
	if pr.Match < n {
		pr.Match = n
		updated = true
	}
	pr.Next = max(pr.Next, n+1)
	// 优化下面代码
	//if pr.Next < n+1 {
	//	pr.Next = n + 1
	//}
	return updated
}

func (pr *Progress) maybeDecrTo(rejected, last uint64) bool {
	if rejected <= pr.Match {
		return false
	}
	pr.Next = max(min(rejected, last+1), 1)
	// 优化下面代码
	//if pr.Next = min(rejected, last+1); pr.Next < 1 {
	//	pr.Next = 1
	//}
	return true
}

// ------------------------------ Raft ------------------------------

// Raft impl
type Raft struct {
	id uint64

	Term uint64
	Vote uint64

	raftLog *Log

	Prs map[uint64]*Progress

	State StateType // this peer's role

	votes map[uint64]bool // votes records

	// msgs need to send
	msgs []pb.Message

	// the leader id
	Lead uint64

	heartbeatTimeout int
	electionTimeout  int
	// randomizedElectionTimeout is a random number between
	// [electiontimeout, 2 * electiontimeout - 1].
	randomizedElectionTimeout int

	heartbeatElapsed int // only leader keeps heartbeatElapsed.
	electionElapsed  int // number of ticks since it reached last electionTimeout

	// leadTransferee is id of the leader transfer target when its value is not zero.
	// 用于集群中 Leader 节点的转移， leadTransferee 记录了 此次 Leader 角色转移的目标节点的 ID 。
	leadTransferee uint64

	// Only one conf change may be pending (in the log, but not yet
	// applied) at a time. This is enforced via PendingConfIndex, which
	// is set to a value >= the log index of the latest pending
	// configuration change (if any). Config changes are only allowed to
	// be proposed if the leader's applied index is greater than this
	// value.
	// (Used in 3A conf change)
	PendingConfIndex uint64
}

// TODO 优化初始化校验步骤
func (r *Raft) loadState(state pb.HardState) {
	if state.Commit < r.raftLog.committed || state.Commit > r.raftLog.LastIndex() {
		panicf("%d state.commit %d is out of range [%d, %d]\n", r.id, state.Commit, r.raftLog.committed, r.raftLog.LastIndex())
	}
	r.raftLog.committed = state.Commit
	r.Term = state.Term
	r.Vote = state.Vote
}

// newRaft return a raft peer with the given config
func newRaft(c *Config) *Raft {
	if err := c.validate(); err != nil {
		panic(err.Error())
	}
	// impl
	raftlog := newLog(c.Storage)
	if c.Applied > 0 {
		raftlog.AppliedTo(c.Applied) // 这里raftlog是指针
	}

	hs, cs, err := c.Storage.InitialState()
	if err != nil {
		panic(err)
	}
	peers := c.peers
	if len(cs.Nodes) > 0 {
		if len(peers) > 0 {
			panic("cannot specify both newRaft (peers) and ConfState.(Nodes)")
		}
		peers = cs.Nodes
	}
	r := &Raft{
		id:               c.ID, //
		Lead:             None,
		raftLog:          raftlog,
		Prs:              make(map[uint64]*Progress),
		electionTimeout:  c.ElectionTick,
		heartbeatTimeout: c.HeartbeatTick,
	}
	for _, p := range peers {
		r.Prs[p] = &Progress{Next: 1}
	}
	if !IsEmptyHardState(hs) {
		r.loadState(hs)
	}

	r.becomeFollower(r.Term, None)

	/*
		// log begin
		var nodesStrs []string
		for _, n := range nodes(r) {
			nodesStrs = append(nodesStrs, fmt.Sprintf("%d", n))
		}
		log.Printf("newRaft %d [peers: [%s], term: %d, commit: %d, applied: %d, lastindex: %d, lastterm: %d]\n",
			r.id, strings.Join(nodesStrs, ","), r.Term, r.raftLog.committed, r.raftLog.applied, r.raftLog.LastIndex(), r.raftLog.LastTerm())
		// log end
	*/
	return r
}

// ---------------------------- used by rawnode ----------------------------

// GetSnap ...
func (r *Raft) GetSnap() *pb.Snapshot { return r.raftLog.pendingSnapshot }

// SoftState ...
func (r *Raft) SoftState() *SoftState { return &SoftState{Lead: r.Lead, RaftState: r.State} }

// HardState ...
func (r *Raft) HardState() pb.HardState {
	return pb.HardState{Term: r.Term, Vote: r.Vote, Commit: r.raftLog.committed}
}

// GetProgress ...
func (r *Raft) GetProgress(id uint64) *Progress { return r.Prs[id] }

// Tick ... used in rawnode.go/Tick
func (r *Raft) Tick() {
	switch r.State {
	case StateFollower, StateCandidate:
		r.tickElection()
	case StateLeader:
		r.tickHeartbeat()
	}
}

// AddNode ... used in rawnode.go/ApplyConfChange
func (r *Raft) AddNode(id uint64) {
	if r.GetProgress(id) == nil {
		r.setProgress(id, 0, r.raftLog.LastIndex()+1)
	} else {
		return
	}
}

// RemoveNode ... used in rawnode.go/ApplyConfChange
func (r *Raft) RemoveNode(id uint64) {
	delete(r.Prs, id)
	if len(r.Prs) == 0 {
		return
	}
	// The quorum size is now smaller, so see if any pending entries can
	// be committed.
	if r.tryCommit() {
		r.bcastAppend()
	}
	// If the removed node is the leadTransferee, then abort the leadership transferring.
	if r.State == StateLeader && r.leadTransferee == id {
		r.abortLeaderTransfer()
	}
}

// ---------------------------- used by raft itself ----------------------------
func (r *Raft) quorum() int                        { return len(r.Prs)/2 + 1 }
func (r *Raft) abortLeaderTransfer()               { r.leadTransferee = None }
func (r *Raft) pastElectionTimeout() bool          { return r.electionElapsed >= r.randomizedElectionTimeout }
func (r *Raft) setProgress(id, match, next uint64) { r.Prs[id] = &Progress{Next: next, Match: match} }
func (r *Raft) forEachProgress(f func(id uint64, pr *Progress)) {
	for id, pr := range r.Prs {
		f(id, pr)
	}
}
func (r *Raft) promotable() bool {
	_, ok := r.Prs[r.id]
	return ok
}

func (r *Raft) resetRandomizedElectionTimeout() {
	r.randomizedElectionTimeout = r.electionTimeout + globalRand.Intn(r.electionTimeout)
}
func (r *Raft) reset(term uint64) {
	if r.Term != term {
		r.Term = term
		r.Vote = None
	}
	r.Lead = None
	r.electionElapsed = 0
	r.heartbeatElapsed = 0
	r.resetRandomizedElectionTimeout()
	r.abortLeaderTransfer()

	r.votes = make(map[uint64]bool)
	r.forEachProgress(func(id uint64, pr *Progress) {
		*pr = Progress{Next: r.raftLog.LastIndex() + 1}
		if id == r.id {
			pr.Match = r.raftLog.LastIndex()
		}
	})

	r.PendingConfIndex = 0
}

// campaign | stepCandidate
func (r *Raft) poll(id uint64, t pb.MessageType, v bool) (granted int) {
	// 投票 v为true投赞成 v为false投反对
	if v {
		log.Printf("%d received %s from %d at term %d\n", r.id, t, id, r.Term)
	} else {
		log.Printf("%d received %s rejection from %d at term %d\n", r.id, t, id, r.Term)
	}
	// 记录投票 返回有效票总数
	if _, ok := r.votes[id]; !ok {
		r.votes[id] = v
	}
	for _, vv := range r.votes {
		if vv {
			granted++
		}
	}
	return granted
}

func (r *Raft) restoreNode(nodes []uint64) {
	for _, n := range nodes {
		match, next := uint64(0), r.raftLog.LastIndex()+1
		if n == r.id {
			match = next - 1
		}
		r.setProgress(n, match, next)
		log.Printf("%d restored progress of %d [%+v]", r.id, n, r.GetProgress(n))
	}
}
func (r *Raft) restore(s pb.Snapshot) bool {
	if s.Metadata.Index <= r.raftLog.committed {
		return false
	}
	if r.raftLog.MatchTerm(s.Metadata.Index, s.Metadata.Term) {
		log.Printf("%d [commit: %d, lastindex: %d, lastterm: %d] fast-forwarded commit to snapshot [index: %d, term: %d]",
			r.id, r.raftLog.committed, r.raftLog.LastIndex(), r.raftLog.LastTerm(), s.Metadata.Index, s.Metadata.Term)
		r.raftLog.CommitTo(s.Metadata.Index)
		return false
	}

	log.Printf("%d [commit: %d, lastindex: %d, lastterm: %d] starts to restore snapshot [index: %d, term: %d]",
		r.id, r.raftLog.committed, r.raftLog.LastIndex(), r.raftLog.LastTerm(), s.Metadata.Index, s.Metadata.Term)

	r.raftLog.Restore(s)
	r.Prs = make(map[uint64]*Progress)
	r.restoreNode(s.Metadata.ConfState.Nodes)
	return true
}

// ---------------- send ----------------
// send 校验消息并加入待发送消息队列
func (r *Raft) send(m pb.Message) {
	m.From = r.id
	if m.MsgType == pb.MessageType_MsgRequestVote || m.MsgType == pb.MessageType_MsgRequestVoteResponse {
		if m.Term == 0 {
			panic(fmt.Sprintf("term should be set when sending %s", m.MsgType))
		}
	} else {
		if m.Term != 0 {
			panic(fmt.Sprintf("term should not be set when sending %s (was %d)", m.MsgType, m.Term))
		}
		if m.MsgType != pb.MessageType_MsgPropose {
			m.Term = r.Term
		}
	}
	r.msgs = append(r.msgs, m)
}

// sendTimeoutNow used in stepLeader
func (r *Raft) sendTimeoutNow(to uint64) {
	r.send(pb.Message{To: to, MsgType: pb.MessageType_MsgTimeoutNow})
}

// 发送消息 附带日志信息
func (r *Raft) sendAppend(to uint64) bool {
	pr := r.GetProgress(to)
	m := pb.Message{}
	m.To = to

	term, errt := r.raftLog.Term(pr.Next - 1)
	ents, erre := r.raftLog.Entries(pr.Next)

	if errt != nil || erre != nil { // send snapshot if we failed to get term or entries
		m.MsgType = pb.MessageType_MsgSnapshot
		// TODO 对snapshot的检验改进
		snapshot, err := r.raftLog.Snapshot()
		if err != nil {
			if err == errSnapshotTemporarilyUnavailable {
				log.Printf("%d failed to send snapshot to %d because snapshot is temporarily unavailable\n", r.id, to)
				return false
			}
			panic(err)
		}
		if IsEmptySnap(&snapshot) {
			panic("need non-empty snapshot")
		}
		// 上面这一段抽象出一个函数
		m.Snapshot = &snapshot
		// 下面是日志
		sindex, sterm := snapshot.Metadata.Index, snapshot.Metadata.Term
		log.Printf("%d [firstindex: %d, commit: %d] sent snapshot[index: %d, term: %d] to %d [%v]\n",
			r.id, r.raftLog.FirstIndex(), r.raftLog.committed, sindex, sterm, to, pr)
		log.Printf("%d paused sending replication messages to %d [%v]\n", r.id, to, pr)
	} else {
		m.MsgType = pb.MessageType_MsgAppend
		m.Index = pr.Next - 1
		m.LogTerm = term

		entries := make([]*pb.Entry, 0, len(ents))
		for i := range ents {
			entries = append(entries, &ents[i])
		}
		m.Entries = entries
		m.Commit = r.raftLog.committed
	}
	r.send(m)
	return true
}

func (r *Raft) sendHeartbeat(to uint64) {
	commit := min(r.GetProgress(to).Match, r.raftLog.committed)
	m := pb.Message{
		To:      to,
		MsgType: pb.MessageType_MsgHeartbeat,
		Commit:  commit,
	}
	r.send(m)
}

// ---------------- boardcast ----------------
func (r *Raft) bcastAppend() {
	r.forEachProgress(func(id uint64, _ *Progress) {
		if id != r.id {
			r.sendAppend(id)
		}
	})
}
func (r *Raft) bcastHeartbeat() {
	r.forEachProgress(func(id uint64, _ *Progress) {
		if id != r.id {
			r.sendHeartbeat(id)
		}
	})
}

// used in appendEntry | stepLeader | removeNode
func (r *Raft) tryCommit() bool {
	matchIndex := make(uint64Slice, len(r.Prs))
	idx := 0
	for _, p := range r.Prs {
		matchIndex[idx] = p.Match
		idx++
	}
	sort.Sort(matchIndex)
	mci := matchIndex[len(matchIndex)-r.quorum()]
	return r.raftLog.TryCommit(mci, r.Term)
}

// becomeLeader | stepLeader
func (r *Raft) appendEntry(es ...pb.Entry) {
	li := r.raftLog.LastIndex()
	for i := range es {
		es[i].Term = r.Term
		es[i].Index = li + 1 + uint64(i)
	}
	// use latest "last" index after truncate/append
	li = r.raftLog.Append(es...)
	r.GetProgress(r.id).maybeUpdate(li)
	// Regardless of maybeCommit's return, our caller will call bcastAppend.
	r.tryCommit()
}

// ---------------- tick ----------------
// run by followers and candidates after r.electionTimeout
func (r *Raft) tickElection() {
	r.electionElapsed++
	if r.promotable() && r.pastElectionTimeout() {
		r.electionElapsed = 0
		r.Step(pb.Message{From: r.id, MsgType: pb.MessageType_MsgHup})
	}
}

// run by leaders to send a MessageType_MsgBeat after r.heartbeatTimeout
func (r *Raft) tickHeartbeat() {
	r.heartbeatElapsed++
	r.electionElapsed++
	if r.electionElapsed >= r.electionTimeout {
		r.electionElapsed = 0
		// If current leader cannot transfer leadership in electionTimeout, it becomes leader again.
		if r.State == StateLeader && r.leadTransferee != None {
			r.abortLeaderTransfer()
		}
	}
	if r.State != StateLeader {
		return
	}
	if r.heartbeatElapsed >= r.heartbeatTimeout {
		r.heartbeatElapsed = 0
		r.Step(pb.Message{From: r.id, MsgType: pb.MessageType_MsgBeat})
	}
}

// ---------------- become ----------------
func (r *Raft) becomeFollower(term uint64, lead uint64) {
	r.reset(term)
	r.Lead = lead
	r.State = StateFollower
	log.Printf("%d became follower at term %d\n", r.id, r.Term)
}
func (r *Raft) becomeCandidate() {
	r.reset(r.Term + 1)
	r.Vote = r.id
	r.State = StateCandidate
	log.Printf("%d became candidate at term %d\n", r.id, r.Term)
}
func (r *Raft) becomeLeader() {
	r.reset(r.Term)
	r.Lead = r.id
	r.State = StateLeader

	r.PendingConfIndex = r.raftLog.LastIndex()

	emptyEnt := pb.Entry{Data: nil}
	r.appendEntry(emptyEnt)
	log.Printf("%d became leader at term %d\n", r.id, r.Term)
}

// Setp | stepFollower
func (r *Raft) campaign() {
	r.becomeCandidate()
	voteMsg := pb.MessageType_MsgRequestVote
	term := r.Term

	if r.quorum() == r.poll(r.id, pb.MessageType_MsgRequestVoteResponse, true) {
		// We won the election after voting for ourselves (which must mean that
		// this is a single-node cluster). Advance to the next state.
		r.becomeLeader()
		return
	}
	// 向集群发起投票请求
	for id := range r.Prs {
		if id == r.id {
			continue
		}
		log.Printf("%d [logterm: %d, index: %d] sent %s request to %d at term %d\n",
			r.id, r.raftLog.LastTerm(), r.raftLog.LastIndex(), voteMsg, id, r.Term)

		r.send(pb.Message{Term: term, To: id, MsgType: voteMsg, Index: r.raftLog.LastIndex(), LogTerm: r.raftLog.LastTerm()})
	}
}

// ---------------------------- step ----------------------------

// Step ... used in tickXXX
func (r *Raft) Step(m pb.Message) error {
	switch {
	case m.Term == 0:
		// local message
	case m.Term > r.Term:
		log.Printf("%d [term: %d] received a %s message with higher term from %d [term: %d]\n",
			r.id, r.Term, m.MsgType, m.From, m.Term)
		if m.MsgType == pb.MessageType_MsgAppend || m.MsgType == pb.MessageType_MsgHeartbeat || m.MsgType == pb.MessageType_MsgSnapshot {
			r.becomeFollower(m.Term, m.From)
		} else {
			r.becomeFollower(m.Term, None)
		}
	case m.Term < r.Term:
		log.Printf("%d [term: %d] ignored a %s message with lower term from %d [term: %d]\n", r.id, r.Term, m.MsgType, m.From, m.Term)
		return nil
	}

	switch m.MsgType {
	case pb.MessageType_MsgHup:
		if r.State != StateLeader {
			ents, err := r.raftLog.Slice(r.raftLog.applied+1, r.raftLog.committed+1)
			if err != nil {
				panicf("unexpected error getting unapplied entries (%v)", err)
			}
			if n := numOfPendingConf(ents); n != 0 && r.raftLog.committed > r.raftLog.applied {
				log.Printf("%d cannot campaign at term %d since there are still %d pending configuration changes to apply\n", r.id, r.Term, n)
				return nil
			}

			log.Printf("%d is starting a new election at term %d\n", r.id, r.Term)

			r.campaign()
		} else {
			log.Printf("%d ignoring MessageType_MsgHup because already leader\n", r.id)
		}

	case pb.MessageType_MsgRequestVote:
		// We can vote if this is a repeat of a vote we've already cast...
		canVote := r.Vote == m.From ||
			// ...we haven't voted and we don't think there's a leader yet in this term...
			(r.Vote == None && r.Lead == None)
		// ...and we believe the candidate is up to date.
		if canVote && r.raftLog.IsUpToDate(m.Index, m.LogTerm) {
			log.Printf("%d [logterm: %d, index: %d, vote: %d] cast %s for %d [logterm: %d, index: %d] at term %d\n",
				r.id, r.raftLog.LastTerm(), r.raftLog.LastIndex(), r.Vote, m.MsgType, m.From, m.LogTerm, m.Index, r.Term)
			r.send(pb.Message{To: m.From, Term: m.Term, MsgType: pb.MessageType_MsgRequestVoteResponse})
			// Only record real votes.
			r.electionElapsed = 0
			r.Vote = m.From
		} else {
			log.Printf("%d [logterm: %d, index: %d, vote: %d] rejected %s from %d [logterm: %d, index: %d] at term %d\n",
				r.id, r.raftLog.LastTerm(), r.raftLog.LastIndex(), r.Vote, m.MsgType, m.From, m.LogTerm, m.Index, r.Term)
			r.send(pb.Message{To: m.From, Term: r.Term, MsgType: pb.MessageType_MsgRequestVoteResponse, Reject: true})
		}

	default:
		switch r.State {
		case StateFollower:
			err := r.stepFollower(m)
			if err != nil {
				return err
			}
		case StateCandidate:
			err := r.stepCandidate(m)
			if err != nil {
				return err
			}
		case StateLeader:
			err := r.stepLeader(m)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

type stepFunc func(r *Raft, m pb.Message) error

// stepLeader handle leader's message
func (r *Raft) stepLeader(m pb.Message) error {
	pr := r.GetProgress(m.From)
	if pr == nil && m.MsgType != pb.MessageType_MsgBeat && m.MsgType != pb.MessageType_MsgPropose {
		log.Printf("%d no progress available for %d\n", r.id, m.From)
		return nil
	}
	switch m.MsgType {
	case pb.MessageType_MsgBeat:
		r.bcastHeartbeat()
		return nil
	case pb.MessageType_MsgPropose:
		if len(m.Entries) == 0 {
			log.Panicf("%d stepped empty MessageType_MsgPropose", r.id)
		}
		if _, ok := r.Prs[r.id]; !ok {
			// If we are not currently a member of the range (i.e. this node
			// was removed from the configuration while serving as leader),
			// drop any new proposals.
			return errProposalDropped
		}
		if r.leadTransferee != None {
			log.Printf("%d [term %d] transfer leadership to %d is in progress; dropping proposal", r.id, r.Term, r.leadTransferee)
			return errProposalDropped
		}

		for i, e := range m.Entries {
			if e.EntryType == pb.EntryType_EntryConfChange {
				if r.PendingConfIndex > r.raftLog.applied {
					log.Printf("propose conf %s ignored since pending unapplied configuration [index %d, applied %d]",
						e.String(), r.PendingConfIndex, r.raftLog.applied)
					m.Entries[i] = &pb.Entry{EntryType: pb.EntryType_EntryNormal}
				} else {
					r.PendingConfIndex = r.raftLog.LastIndex() + uint64(i) + 1
				}
			}
		}

		es := make([]pb.Entry, 0, len(m.Entries))
		for _, e := range m.Entries {
			es = append(es, *e)
		}

		r.appendEntry(es...)
		r.bcastAppend()
		return nil
	case pb.MessageType_MsgAppendResponse:
		if m.Reject {
			log.Printf("%d received MessageType_MsgAppend rejection(lastindex: %d) from %d for index %d",
				r.id, m.RejectHint, m.From, m.Index)
			if pr.maybeDecrTo(m.Index, m.RejectHint) {
				r.sendAppend(m.From)
			}
		} else {
			if pr.maybeUpdate(m.Index) {
				if r.tryCommit() {
					r.bcastAppend()
				}
				// Transfer leadership is in progress.
				if m.From == r.leadTransferee && pr.Match == r.raftLog.LastIndex() {
					log.Printf("%d sent MessageType_MsgTimeoutNow to %d after received MessageType_MsgAppendResponse", r.id, m.From)
					r.sendTimeoutNow(m.From)
				}
			}
		}
	case pb.MessageType_MsgHeartbeatResponse:
		if pr.Match < r.raftLog.LastIndex() {
			r.sendAppend(m.From)
		}
	case pb.MessageType_MsgTransferLeader:
		leadTransferee := m.From
		lastLeadTransferee := r.leadTransferee
		if lastLeadTransferee != None {
			if lastLeadTransferee == leadTransferee {
				log.Printf("%d [term %d] transfer leadership to %d is in progress, ignores request to same node %d",
					r.id, r.Term, leadTransferee, leadTransferee)
				return nil
			}
			r.abortLeaderTransfer()
			log.Printf("%d [term %d] abort previous transferring leadership to %d", r.id, r.Term, lastLeadTransferee)
		}
		if leadTransferee == r.id {
			log.Printf("%d is already leader. Ignored transferring leadership to self", r.id)
			return nil
		}
		// Transfer leadership to third party.
		log.Printf("%d [term %d] starts to transfer leadership to %d", r.id, r.Term, leadTransferee)
		// Transfer leadership should be finished in one electionTimeout, so reset r.electionElapsed.
		r.electionElapsed = 0
		r.leadTransferee = leadTransferee
		if pr.Match == r.raftLog.LastIndex() {
			r.sendTimeoutNow(leadTransferee)
			log.Printf("%d sends MessageType_MsgTimeoutNow to %d immediately as %d already has up-to-date log", r.id, leadTransferee, leadTransferee)
		} else {
			r.sendAppend(leadTransferee)
		}
	}
	return nil
}

// stepCandidate handle candidate's message
func (r *Raft) stepCandidate(m pb.Message) error {
	switch m.MsgType {
	case pb.MessageType_MsgPropose:
		log.Printf("%d no leader at term %d; dropping proposal", r.id, r.Term)
		return errProposalDropped
	case pb.MessageType_MsgAppend:
		r.becomeFollower(m.Term, m.From) // always m.Term == r.Term
		r.handleAppendEntries(m)
	case pb.MessageType_MsgHeartbeat:
		r.becomeFollower(m.Term, m.From) // always m.Term == r.Term
		r.handleHeartbeat(m)
	case pb.MessageType_MsgSnapshot:
		r.becomeFollower(m.Term, m.From) // always m.Term == r.Term
		r.handleSnapshot(m)
	case pb.MessageType_MsgRequestVoteResponse:
		gr := r.poll(m.From, m.MsgType, !m.Reject)
		log.Printf("%d [quorum:%d] has received %d %s votes and %d vote rejections", r.id, r.quorum(), gr, m.MsgType, len(r.votes)-gr)
		switch r.quorum() {
		case gr:
			r.becomeLeader()
			r.bcastAppend()
		case len(r.votes) - gr:
			// m.Term > r.Term; reuse r.Term
			r.becomeFollower(r.Term, None)
		}
	case pb.MessageType_MsgTimeoutNow:
		log.Printf("%d [term %d state %v] ignored MessageType_MsgTimeoutNow from %d", r.id, r.Term, r.State, m.From)
	}
	return nil
}

// stepFollower handle follower's message
func (r *Raft) stepFollower(m pb.Message) error {
	switch m.MsgType {
	case pb.MessageType_MsgPropose:
		log.Printf("%d is no leader at term %d; dropping proposal", r.id, r.Term)
		return errProposalDropped
	case pb.MessageType_MsgAppend:
		r.electionElapsed = 0
		r.Lead = m.From
		r.handleAppendEntries(m)
	case pb.MessageType_MsgHeartbeat:
		r.electionElapsed = 0
		r.Lead = m.From
		r.handleHeartbeat(m)
	case pb.MessageType_MsgSnapshot:
		r.electionElapsed = 0
		r.Lead = m.From
		r.handleSnapshot(m)
	case pb.MessageType_MsgTransferLeader:
		if r.Lead == None {
			log.Printf("%d no leader at term %d; dropping leader transfer msg", r.id, r.Term)
			return nil
		}
		m.To = r.Lead
		r.send(m)
	case pb.MessageType_MsgTimeoutNow:
		if r.promotable() {
			log.Printf("%d [term %d] received MessageType_MsgTimeoutNow from %d and starts an election to get leadership.", r.id, r.Term, m.From)
			r.campaign()
		} else {
			log.Printf("%d received MessageType_MsgTimeoutNow from %d but is not promotable", r.id, m.From)
		}
	}
	return nil
}

// ---------------------------- handle ----------------------------

func (r *Raft) handleAppendEntries(m pb.Message) {
	// m.Index表示leader发送给follower的上一条日志的索引位置
	// 如果当前follower在该位置的日志已经提交过了(有可能该leader是刚选举产生的 没有follower的日志信息故设置m.Index=0 这里就拿到0)
	// 则把follower当前提日志交的索引位置告诉leader 让leader从该follower提交位置的下一条位置的日志开始发送给follower
	if m.Index < r.raftLog.committed {
		r.send(pb.Message{To: m.From, MsgType: pb.MessageType_MsgAppendResponse, Index: r.raftLog.committed})
		return
	}

	ents := make([]pb.Entry, 0, len(m.Entries))
	for _, ent := range m.Entries {
		ents = append(ents, *ent)
	}
	// 将日志追加到follower的日志中 此时可能存在冲突(raftLog内部解决)
	if mlastIndex, ok := r.raftLog.TryAppend(m.Index, m.LogTerm, m.Commit, ents...); ok {
		// 追加成功 返回最新idx
		r.send(pb.Message{To: m.From, MsgType: pb.MessageType_MsgAppendResponse, Index: mlastIndex})
	} else {
		// leader与follower的日志没有匹配 那么把follower的最新日志的索引位置告诉leader
		// 以便leader下一次从该follower的最新日志位置之后开始尝试发送附加日志
		log.Printf("%d [logterm: %d, index: %d] rejected MessageType_MsgAppend [logterm: %d, index: %d] from %d",
			r.id, zeroTermOnRangeErr(r.raftLog.Term(m.Index)), m.Index, m.LogTerm, m.Index, m.From)
		r.send(pb.Message{To: m.From, MsgType: pb.MessageType_MsgAppendResponse, Index: m.Index, Reject: true, RejectHint: r.raftLog.LastIndex()})
	}
}

func (r *Raft) handleHeartbeat(m pb.Message) {
	r.raftLog.CommitTo(m.Commit)
	r.send(pb.Message{To: m.From, MsgType: pb.MessageType_MsgHeartbeatResponse})
}

func (r *Raft) handleSnapshot(m pb.Message) {
	sindex, sterm := m.Snapshot.Metadata.Index, m.Snapshot.Metadata.Term
	if r.restore(*m.Snapshot) {
		log.Printf("%d [commit: %d] restored snapshot [index: %d, term: %d]",
			r.id, r.raftLog.committed, sindex, sterm)
		r.send(pb.Message{To: m.From, MsgType: pb.MessageType_MsgAppendResponse, Index: r.raftLog.LastIndex()})
	} else {
		log.Printf("%d [commit: %d] ignored snapshot [index: %d, term: %d]",
			r.id, r.raftLog.committed, sindex, sterm)
		r.send(pb.Message{To: m.From, MsgType: pb.MessageType_MsgAppendResponse, Index: r.raftLog.committed})
	}
}
