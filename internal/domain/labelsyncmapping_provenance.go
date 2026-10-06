package domain

// LabelSyncProvenanceList is a grouped view of current mapping-owned label keys.
type LabelSyncProvenanceList struct {
	Items []LabelSyncProvenanceItem
}

// LabelSyncProvenanceItem contains the current mapping names that own one exact key.
type LabelSyncProvenanceItem struct {
	Key    string
	Owners []string
}
