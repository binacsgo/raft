package raft

import (
	pb "github.com/binacsgo/raft/eraftpb"
)

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

// Progress follower’s progress in the view of the leader
type Progress struct {
	Match, Next uint64
}

// Raft impl
type Raft struct {
	id uint64

	Term uint64
	Vote uint64

	RaftLog *Log

	Prs map[uint64]*Progress

	State StateType // this peer's role

	votes map[uint64]bool // votes records

	// msgs need to send
	msgs []pb.Message

	// the leader id
	Lead uint64

	heartbeatTimeout int
	electionTimeout  int
	heartbeatElapsed int // only leader keeps heartbeatElapsed.
	electionElapsed  int // number of ticks since it reached last electionTimeout

	// leadTransferee is id of the leader transfer target when its value is not zero.
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

// newRaft return a raft peer with the given config
func newRaft(c *Config) *Raft {
	if err := c.validate(); err != nil {
		panic(err.Error())
	}
	// impl
	return nil
}

func (r *Raft) sendAppend(to uint64) bool {
	return false
}

func (r *Raft) sendHeartbeat(to uint64) {
}

func (r *Raft) tick() {
}

func (r *Raft) becomeFollower(term uint64, lead uint64) {
}

func (r *Raft) becomeCandidate() {
}

func (r *Raft) becomeLeader() {
}

// Step ...
func (r *Raft) Step(m pb.Message) error {
	switch r.State {
	case StateFollower:
	case StateCandidate:
	case StateLeader:
	}
	return nil
}

func (r *Raft) handleAppendEntries(m pb.Message) {
}

func (r *Raft) handleHeartbeat(m pb.Message) {
}

func (r *Raft) handleSnapshot(m pb.Message) {
}

func (r *Raft) addNode(id uint64) {
}

func (r *Raft) removeNode(id uint64) {
}
