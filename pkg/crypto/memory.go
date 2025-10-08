package crypto

// SecureMemoryWipe securely wipes sensitive data from memory by overwriting with zeros.
// This helps prevent sensitive data from remaining in memory after use, reducing the
// window of opportunity for memory-based attacks or accidental disclosure.
//
// Note: This provides defense-in-depth but cannot guarantee the data is removed from all
// copies that may exist due to compiler optimizations, swap space, or core dumps.
// For file-based wiping, use fileio.OverwriteAndWipe.
func SecureMemoryWipe(data []byte) {
	for i := range data {
		data[i] = 0
	}
}
