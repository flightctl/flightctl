package v1alpha1

func (s *CatalogItemSpec) FindVersion(version string) *CatalogItemVersion {
	for i := range s.Versions {
		if s.Versions[i].Version == version {
			return &s.Versions[i]
		}
	}
	return nil
}

func (s *CatalogItemSpec) FindArtifact(artifactType CatalogItemArtifactType) *CatalogItemArtifact {
	for i := range s.Artifacts {
		if s.Artifacts[i].Type == artifactType {
			return &s.Artifacts[i]
		}
	}
	return nil
}
