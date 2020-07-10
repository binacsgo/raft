package raft

import (
	"github.com/golang/protobuf/proto"

	pb "github.com/binacsgo/raft/eraftpb"
)

// ---------------------------- ready ----------------------------

// SoftState provides state that is useful for logging and debugging.
// The state is volatile and does not need to be persisted to the WAL.
type SoftState struct {
	Lead      uint64 // must use atomic operations to access; keep 64-bit aligned.
	RaftState StateType
}

func (s *SoftState) equal(ss *SoftState) bool {
	return s.Lead == ss.Lead && s.RaftState == ss.RaftState
}

// Ready 封装 [准备读,准备保存,准备提交,发送至其他节点]的消息和条目 所有成员只读
type Ready struct {
	*SoftState
	// The current state of a Node to be saved to stable storage BEFORE
	// Messages are sent.
	pb.HardState

	// Entries specifies entries to be saved to stable storage BEFORE
	// Messages are sent.
	Entries []pb.Entry

	// Snapshot specifies the snapshot to be saved to stable storage.
	Snapshot pb.Snapshot

	// CommittedEntries specifies entries to be committed to a
	// store/state-machine. These have previously been committed to stable
	// store.
	CommittedEntries []pb.Entry

	// Messages specifies outbound messages to be sent AFTER Entries are
	// committed to stable storage.
	// If it contains a MessageType_MsgSnapshot message, the application MUST report back to raft
	// when the snapshot has been received or has failed by calling ReportSnapshot.
	Messages []pb.Message
}

func newReady(r *Raft, prevSoftSt *SoftState, prevHardSt pb.HardState) Ready {
	rd := Ready{
		Entries:          r.raftLog.UnstableEntries(),
		CommittedEntries: r.raftLog.SliceNotApplied(),
	}
	if len(r.msgs) != 0 {
		rd.Messages = r.msgs
		r.msgs = nil
	}
	if softSt := r.SoftState(); !softSt.equal(prevSoftSt) {
		rd.SoftState = softSt
	}
	if hardSt := r.HardState(); !isHardStateEqual(hardSt, prevHardSt) {
		rd.HardState = hardSt
	}
	if r.raftLog.pendingSnapshot != nil {
		rd.Snapshot = *r.raftLog.pendingSnapshot
	}
	return rd
}

// ---------------------------- rawnode ----------------------------

// RawNode a wrapper of Raft.
type RawNode struct {
	Raft *Raft
	// Data.
	prevSoftSt *SoftState
	prevHardSt pb.HardState
}

// NewRawNode returns a new RawNode
func NewRawNode(config *Config) (*RawNode, error) {
	if config.ID == 0 {
		panic("config.ID must not be zero")
	}
	r := newRaft(config)
	rn := &RawNode{
		Raft:       r,
		prevSoftSt: r.SoftState(),
		prevHardSt: r.HardState(),
	}
	return rn, nil
}

// Tick advances the internal logical clock by a single tick.
func (rn *RawNode) Tick() {
	rn.Raft.Tick()
}

// Campaign 驱动节点至 candidate state.
func (rn *RawNode) Campaign() error {
	return rn.Raft.Step(pb.Message{
		MsgType: pb.MessageType_MsgHup,
	})
}

// Propose proposes data
func (rn *RawNode) Propose(data []byte) error {
	ent := pb.Entry{Data: data}
	return rn.Raft.Step(pb.Message{
		MsgType: pb.MessageType_MsgPropose,
		From:    rn.Raft.id,
		Entries: []*pb.Entry{&ent}})
}

// ProposeConfChange proposes a config change.
// USE *pb.ConfChange instean of pb.ConfChange AND use proto.Marshal
func (rn *RawNode) ProposeConfChange(cc *pb.ConfChange) error {
	data, err := proto.Marshal(cc)
	if err != nil {
		return err
	}
	ent := pb.Entry{EntryType: pb.EntryType_EntryConfChange, Data: data}
	return rn.Raft.Step(pb.Message{
		MsgType: pb.MessageType_MsgPropose,
		Entries: []*pb.Entry{&ent},
	})
}

// ApplyConfChange applies a config change to the local node.
func (rn *RawNode) ApplyConfChange(cc *pb.ConfChange) *pb.ConfState {
	if cc.NodeId == None {
		return &pb.ConfState{Nodes: nodes(rn.Raft)}
	}
	switch cc.ChangeType {
	case pb.ConfChangeType_AddNode:
		rn.Raft.AddNode(cc.NodeId)
	case pb.ConfChangeType_RemoveNode:
		rn.Raft.RemoveNode(cc.NodeId)
	default:
		panic("unexpected conf type")
	}
	return &pb.ConfState{Nodes: nodes(rn.Raft)}
}

// Step advances the state machine using the given message.
func (rn *RawNode) Step(m pb.Message) error {
	// ignore unexpected local messages receiving over network
	if IsLocalMsg(m.MsgType) {
		return errStepLocalMsg
	}
	if pr := rn.Raft.Prs[m.From]; pr != nil || !IsResponseMsg(m.MsgType) {
		return rn.Raft.Step(m)
	}
	return errStepPeerNotFound
}

// GetSnap return the snapshot from raft
func (rn *RawNode) GetSnap() *pb.Snapshot {
	return rn.Raft.GetSnap()
}

// Ready returns the current point-in-time state of this RawNode.
func (rn *RawNode) Ready() Ready {
	rd := newReady(rn.Raft, rn.prevSoftSt, rn.prevHardSt)
	rn.Raft.msgs = nil // attention
	return rd
}

// HasReady called when RawNode user need to check if any Ready pending.
func (rn *RawNode) HasReady() bool {
	r := rn.Raft
	if !r.SoftState().equal(rn.prevSoftSt) {
		return true
	}
	if hardSt := r.HardState(); !IsEmptyHardState(hardSt) && !isHardStateEqual(hardSt, rn.prevHardSt) {
		return true
	}
	if snap := rn.GetSnap(); snap != nil && IsEmptySnap(snap) {
		return true
	}
	if len(r.msgs) > 0 || len(r.raftLog.UnstableEntries()) > 0 || r.raftLog.HasSliceNotApplied() {
		return true
	}
	return false
}

// Advance notifies the RawNode that the application has applied and saved progress in the
// last Ready results.
func (rn *RawNode) Advance(rd Ready) {
	if rd.SoftState != nil {
		rn.prevSoftSt = rd.SoftState
	}
	if !IsEmptyHardState(rd.HardState) {
		rn.prevHardSt = rd.HardState
	}
	if rn.prevHardSt.Commit != 0 {
		rn.Raft.raftLog.AppliedTo(rn.prevHardSt.Commit)
	}
	if len(rd.Entries) > 0 {
		e := rd.Entries[len(rd.Entries)-1]
		rn.Raft.raftLog.StableTo(e.Index, e.Term)
	}
	if !IsEmptySnap(&rd.Snapshot) {
		rn.Raft.raftLog.StableSnapTo(rd.Snapshot.Metadata.Index)
	}
}

// GetProgress return the the Progress of this node and its peers, if this
// node is leader.
func (rn *RawNode) GetProgress() map[uint64]Progress {
	prs := make(map[uint64]Progress)
	if rn.Raft.State == StateLeader {
		for id, p := range rn.Raft.Prs {
			prs[id] = *p
		}
	}
	return prs
}

// TransferLeader tries to transfer leadership to the given transferee.
func (rn *RawNode) TransferLeader(transferee uint64) {
	_ = rn.Raft.Step(pb.Message{MsgType: pb.MessageType_MsgTransferLeader, From: transferee})
}
