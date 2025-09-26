package main

import (
	"fmt"
	"strconv"
	"strings"
)

func extractWorkerNumber(workerID string) int {
	// Handle composite format: worker<VMID>_proc<GinkgoProcID>
	parts := strings.Split(workerID, "_")
	var workerNum, procNum int = 1, 1 // Defaults

	for _, part := range parts {
		if strings.HasPrefix(part, "worker") {
			// Extract the number after "worker"
			if num, err := strconv.Atoi(strings.TrimPrefix(part, "worker")); err == nil {
				workerNum = num
			}
		} else if strings.HasPrefix(part, "proc") {
			// Extract the number after "proc"
			if num, err := strconv.Atoi(strings.TrimPrefix(part, "proc")); err == nil {
				procNum = num
			}
		}
	}

	// Calculate port offset: (workerNum * 10) + procNum
	// This automatically separates main workers (1-9) from device simulation workers (10+)
	return (workerNum * 10) + procNum
}

func main() {
	fmt.Println("Port calculations:")
	fmt.Println("Main workers:")
	fmt.Printf("  worker1_proc1: %d\n", extractWorkerNumber("worker1_proc1"))
	fmt.Printf("  worker2_proc1: %d\n", extractWorkerNumber("worker2_proc1"))
	fmt.Printf("  worker3_proc1: %d\n", extractWorkerNumber("worker3_proc1"))
	fmt.Println("Device simulation workers:")
	fmt.Printf("  worker10_proc0: %d\n", extractWorkerNumber("worker10_proc0"))
	fmt.Printf("  worker10_proc1: %d\n", extractWorkerNumber("worker10_proc1"))
	fmt.Printf("  worker10_proc2: %d\n", extractWorkerNumber("worker10_proc2"))
	fmt.Printf("  worker10_proc3: %d\n", extractWorkerNumber("worker10_proc3"))
}
