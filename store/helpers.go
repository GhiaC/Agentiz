package store

import (
	"strconv"
	"strings"
)

// parseToolSeqFromToolID extracts sequence number from ToolID.
// Format: {SessionID}-t{SeqID} e.g. "user-core-s0001-t0001" -> 1
func parseToolSeqFromToolID(toolID string) int {
	idx := strings.LastIndex(toolID, "-t")
	if idx == -1 || idx+2 >= len(toolID) {
		return 0
	}
	seq, err := strconv.Atoi(toolID[idx+2:])
	if err != nil {
		return 0
	}
	return seq
}
