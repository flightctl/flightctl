package systeminfo

import (
	"bufio"
	"bytes"
	"fmt"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
)

// collectCPUInfo gathers CPU information
func collectCPUInfo(reader fileio.Reader) (*CPUInfo, error) {
	content, err := reader.ReadFile(cpuInfoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %v", cpuInfoPath, err)
	}

	cpuInfo := &CPUInfo{
		Architecture: runtime.GOARCH,
		Processors:   []ProcessorInfo{},
	}

	physicalProcessors, totalCores := parseProcessorBlocks(content)
	cpuInfo.TotalCores = totalCores
	populateCPUInfo(cpuInfo, physicalProcessors)

	// handle no processors found
	if len(cpuInfo.Processors) == 0 {
		err := handleEmptyProcessors(reader, cpuInfo, content)
		if err != nil {
			return nil, err
		}
	}

	return cpuInfo, nil
}


func splitProcessorBlocks(content []byte) [][]byte {
	lines := bytes.Split(content, []byte("\n"))
	var blocks [][]byte
	var currentBlock []byte

	for _, line := range lines {
		if bytes.HasPrefix(line, []byte("processor")) && len(currentBlock) > 0 {
			blocks = append(blocks, currentBlock)
			currentBlock = nil
		}
		currentBlock = append(currentBlock, line...)
		currentBlock = append(currentBlock, '\n')
	}
	if len(currentBlock) > 0 {
		blocks = append(blocks, currentBlock)
	}
	return blocks
}

// parseProcessorBlocks parses the processor blocks from the file content
func parseProcessorBlocks(content []byte) (map[int]*ProcessorInfo, int) {
	physicalProcessors := make(map[int]*ProcessorInfo)
	coresSeen := make(map[int]map[int]bool)
	totalCores := 0

	blocks := splitProcessorBlocks(content)

	for blockIndex, block := range blocks {
		if len(bytes.TrimSpace(block)) == 0 {
			continue
		}

		physicalID, coreID, vendor, model, processorID := parseProcessorBlock(block)

		if physicalID < 0 {
			if processorID >= 0 {
				physicalID = processorID
			} else {
				physicalID = blockIndex
			}
		}

		if coreID < 0 {
			if processorID >= 0 {
				coreID = processorID
			} else {
				coreID = blockIndex
			}
		}

		proc, exists := physicalProcessors[physicalID]
		if !exists {
			proc = &ProcessorInfo{
				ID:                physicalID,
				NumCores:          0,
				NumThreads:        0,
				NumThreadsPerCore: 0,
				Vendor:            vendor,
				Model:             model,
			}
			physicalProcessors[physicalID] = proc
			coresSeen[physicalID] = make(map[int]bool)
		} else {
			if proc.Vendor == "" {
				proc.Vendor = vendor
			}
			if proc.Model == "" {
				proc.Model = model
			}
		}

		proc.NumThreads++
		if !coresSeen[physicalID][coreID] {
			coresSeen[physicalID][coreID] = true
			proc.NumCores++
			totalCores++
		}
	}

	return physicalProcessors, totalCores
}

func parseProcessorBlock(block []byte) (int, int, string, string, int) {
	var physicalID, coreID = -1, -1
	var processorID = -1
	var vendor, model string

	lines := bytes.Split(block, []byte("\n"))
	for _, line := range lines {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}

		idx := bytes.IndexByte(line, ':')
		if idx < 0 {
			continue
		}

		key := string(bytes.TrimSpace(line[:idx]))
		value := string(bytes.TrimSpace(line[idx+1:]))

		switch key {
		case "physical id":
			id, err := strconv.Atoi(value)
			if err == nil {
				physicalID = id
			}
		case "core id":
			id, err := strconv.Atoi(value)
			if err == nil {
				coreID = id
			}
		case "processor":
			id, err := strconv.Atoi(value)
			if err == nil {
				processorID = id
			}
		case "vendor_id":
			vendor = value
		case "model name":
			model = value
		}
	}

	return physicalID, coreID, vendor, model, processorID
}

// populateCPUInfo populates the CPU info from the processor data
func populateCPUInfo(cpuInfo *CPUInfo, physicalProcessors map[int]*ProcessorInfo) {
	cpuInfo.TotalThreads = 0

	// ensure consistent ordering of processors
	var procIDs []int
	for id := range physicalProcessors {
		procIDs = append(procIDs, id)
	}
	sort.Ints(procIDs)

	for _, id := range procIDs {
		proc := physicalProcessors[id]
		cpuInfo.TotalThreads += proc.NumThreads

		// calc threads per core
		if proc.NumCores > 0 && proc.NumThreads >= proc.NumCores {
			proc.NumThreadsPerCore = proc.NumThreads / proc.NumCores
		} else if proc.NumThreads > 0 {
			// default to 1 thread per core if we couldn't count cores
			proc.NumThreadsPerCore = 1
			proc.NumCores = proc.NumThreads
		}

		cpuInfo.Processors = append(cpuInfo.Processors, *proc)
	}

	// best effort edge cases for total core count
	if cpuInfo.TotalCores == 0 && cpuInfo.TotalThreads > 0 {
		cpuInfo.TotalCores = len(physicalProcessors)
		if cpuInfo.TotalCores == 0 {
			cpuInfo.TotalCores = cpuInfo.TotalThreads
		}
	}
}

// handleEmptyProcessors handles the case where no processors were found
func handleEmptyProcessors(reader fileio.Reader, cpuInfo *CPUInfo, content []byte) error {
	vendor, model := extractBasicCPUInfo(content)

	if cpuInfo.TotalCores == 0 {
		cpuInfo.TotalCores = 1
	}
	if cpuInfo.TotalThreads == 0 {
		cpuInfo.TotalThreads = 1
	}

	cpuInfo.Processors = append(cpuInfo.Processors, ProcessorInfo{
		ID:                0,
		NumCores:          cpuInfo.TotalCores,
		NumThreads:        cpuInfo.TotalThreads,
		NumThreadsPerCore: max(1, cpuInfo.TotalThreads/cpuInfo.TotalCores),
		Vendor:            vendor,
		Model:             model,
	})

	return nil
}

// extractBasicCPUInfo extracts basic CPU info from content
func extractBasicCPUInfo(content []byte) (string, string) {
	scanner := bufio.NewScanner(bytes.NewReader(content))
	vendor := ""
	model := ""

	for scanner.Scan() {
		line := scanner.Text()

		if strings.HasPrefix(line, "vendor_id") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				vendor = strings.TrimSpace(parts[1])
			}
		} else if strings.HasPrefix(line, "model name") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				model = strings.TrimSpace(parts[1])
			}
		}

		if vendor != "" && model != "" {
			break
		}
	}

	return vendor, model
}

func max(x, y int) int {
	if x > y {
		return x
	}
	return y
}
