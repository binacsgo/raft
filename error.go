package raft

import "errors"

// raft
var (
	errProposalDropped = errors.New("raft proposal dropped")

	errConfigValidateIDisNone      = errors.New("cannot use none as id")
	errConfigValidateHeartbeatTick = errors.New("heartbeat tick must be greater than 0")
	errConfigValidateElectionTick  = errors.New("election tick must be greater than heartbeat tick")
	errConfigValidateStorageNil    = errors.New("storage cannot be nil")
)

// rawnode
var (
	errStepLocalMsg     = errors.New("raft: cannot step raft local message")
	errStepPeerNotFound = errors.New("raft: cannot step as peer not found")
)

// storage
var (
	errCompacted                      = errors.New("requested index is unavailable due to compaction")
	errSnapOutOfDate                  = errors.New("requested index is older than the existing snapshot")
	errUnavailable                    = errors.New("requested entry at index is unavailable")
	errSnapshotTemporarilyUnavailable = errors.New("snapshot is temporarily unavailable")
)
