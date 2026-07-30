package ui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/guilhermehto/cogitator/internal/state"
)

func unreachableFooter(unreachable []state.InstanceFailure) string {
	if len(unreachable) == 0 {
		return ""
	}

	rows := append([]state.InstanceFailure(nil), unreachable...)
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Host == rows[j].Host {
			if rows[i].Port == rows[j].Port {
				return rows[i].InstanceID < rows[j].InstanceID
			}
			return rows[i].Port < rows[j].Port
		}
		return rows[i].Host < rows[j].Host
	})

	parts := make([]string, 0, len(rows))
	for _, inst := range rows {
		parts = append(parts, fmt.Sprintf("%s:%d (%d consecutive failures)", inst.Host, inst.Port, inst.ConsecutiveFailures))
	}

	noun := "instances"
	if len(rows) == 1 {
		noun = "instance"
	}
	return attnErrStyle.Render(fmt.Sprintf("⚠ %d %s unreachable: %s", len(rows), noun, strings.Join(parts, ", ")))
}
