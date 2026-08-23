// Package coordinator implements the quorum orchestrator for Dynamo-style operations.
//
// Architecture:
//
//	                ┌──────────────────────────────────┐
//	                │           Coordinator            │
//	                │ (Single Reactor Thread Event Loop)│
//	                └─────────────────┬────────────────┘
//	                                  │
//	       ┌──────────────┬───────────┼───────────┬──────────────┐
//	       ▼              ▼           ▼           ▼              ▼
//	┌─────────────┐ ┌───────────┐ ┌───────┐ ┌───────────┐ ┌─────────────┐
//	│  HashRing   │ │  Storage  │ │Transp.│ │ReadRepair │ │ AntiEntropy │
//	│ (Partitions)│ │  Adapter  │ │Adapter│ │ (Repair)  │ │(Merkle Tree)│
//	└─────────────┘ └───────────┘ └───────┘ └───────────┘ └─────────────┘
package coordinator
