package simulate

import "sort"

type Node struct {
	Name            string
	AllocatableCPU  int64
	AllocatableMem  int64
	AllocatedCPU    int64
	AllocatedMem    int64
	Schedulable     bool
	UnschedulableBy string
}

func (n Node) FreeCPU() int64 {
	return max64(n.AllocatableCPU-n.AllocatedCPU, 0)
}

func (n Node) FreeMem() int64 {
	return max64(n.AllocatableMem-n.AllocatedMem, 0)
}

type Pod struct {
	Name string
	CPU  int64
	Mem  int64
}

type Placement struct {
	Placed         int
	Unplaced       []Pod
	NodesExhausted []string
	FreeAfter      map[string]Node
}

func Place(nodes []Node, pods []Pod) Placement {
	remaining := make([]Node, 0, len(nodes))
	for _, node := range nodes {
		if !node.Schedulable {
			continue
		}
		remaining = append(remaining, node)
	}

	ordered := make([]Pod, len(pods))
	copy(ordered, pods)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].CPU != ordered[j].CPU {
			return ordered[i].CPU > ordered[j].CPU
		}
		return ordered[i].Mem > ordered[j].Mem
	})

	placement := Placement{FreeAfter: map[string]Node{}}
	exhausted := map[string]bool{}

	for _, pod := range ordered {
		index := bestFit(remaining, pod)
		if index < 0 {
			placement.Unplaced = append(placement.Unplaced, pod)
			continue
		}

		remaining[index].AllocatedCPU += pod.CPU
		remaining[index].AllocatedMem += pod.Mem
		placement.Placed++

		if remaining[index].FreeCPU() == 0 || remaining[index].FreeMem() == 0 {
			exhausted[remaining[index].Name] = true
		}
	}

	for _, node := range remaining {
		placement.FreeAfter[node.Name] = node
		if exhausted[node.Name] {
			placement.NodesExhausted = append(placement.NodesExhausted, node.Name)
		}
	}

	sort.Strings(placement.NodesExhausted)

	return placement
}

func bestFit(nodes []Node, pod Pod) int {
	best := -1
	var bestSlack int64

	for i, node := range nodes {
		if node.FreeCPU() < pod.CPU || node.FreeMem() < pod.Mem {
			continue
		}

		slack := node.FreeCPU() - pod.CPU
		if best < 0 || slack < bestSlack {
			best, bestSlack = i, slack
		}
	}

	return best
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}

	return b
}
