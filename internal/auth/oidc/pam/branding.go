//go:build linux

package pam

import (
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sirupsen/logrus"
)

const (
	maxImageSize = 1 << 20   // 1 MB
	maxCSSSize   = 256 << 10 // 256 KB
)

// BrandingAssets holds all branding assets loaded from a drop-in directory at startup.
type BrandingAssets struct {
	DisplayName    string
	FaviconPath    string // URL path served to the browser (e.g. "/auth/branding/favicon.png")
	FaviconBytes   []byte // nil = use embedded default
	FaviconMIME    string
	LightLogoPath  string
	LightLogoBytes []byte
	LightLogoMIME  string
	DarkLogoPath   string
	DarkLogoBytes  []byte
	DarkLogoMIME   string
	CSSFiles       []BrandingCSS // sorted by filename
}

// BrandingCSS represents a single CSS override file from the branding directory.
type BrandingCSS struct {
	Name    string // filename (e.g. "01-colors.css")
	Content []byte
}

// LoadBrandingAssets reads branding assets from brandingDir at startup.
// All files are optional; missing directory or files silently fall back to defaults.
func LoadBrandingAssets(brandingDir, displayName string, log *logrus.Logger) (*BrandingAssets, error) {
	assets := &BrandingAssets{
		DisplayName:   displayName,
		FaviconPath:   defaultFaviconSrc,
		LightLogoPath: defaultLogoSrc,
		DarkLogoPath:  defaultLogoSrc,
	}

	if brandingDir == "" {
		return assets, nil
	}

	info, err := os.Stat(brandingDir)
	if err != nil {
		if os.IsNotExist(err) {
			log.Debugf("branding directory %q does not exist, using defaults", brandingDir)
			return assets, nil
		}
		return nil, fmt.Errorf("stat branding directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("branding path is not a directory: %s", brandingDir)
	}

	entries, err := os.ReadDir(brandingDir)
	if err != nil {
		return nil, fmt.Errorf("reading branding directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		path := filepath.Join(brandingDir, name)
		lower := strings.ToLower(name)

		switch {
		case strings.HasSuffix(lower, ".css"):
			if err := loadCSSFile(assets, name, path); err != nil {
				return nil, err
			}
			log.Infof("loaded custom branding CSS: %s", name)
		case matchesPrefix(lower, "favicon"):
			if err := loadImageFile(&assets.FaviconBytes, &assets.FaviconMIME, &assets.FaviconPath, name, path, maxImageSize); err != nil {
				return nil, fmt.Errorf("loading favicon %q: %w", name, err)
			}
			log.Infof("loaded custom favicon: %s", name)
		case matchesPrefix(lower, "logo-light"):
			if err := loadImageFile(&assets.LightLogoBytes, &assets.LightLogoMIME, &assets.LightLogoPath, name, path, maxImageSize); err != nil {
				return nil, fmt.Errorf("loading light logo %q: %w", name, err)
			}
			log.Infof("loaded custom light-theme logo: %s", name)
		case matchesPrefix(lower, "logo-dark"):
			if err := loadImageFile(&assets.DarkLogoBytes, &assets.DarkLogoMIME, &assets.DarkLogoPath, name, path, maxImageSize); err != nil {
				return nil, fmt.Errorf("loading dark logo %q: %w", name, err)
			}
			log.Infof("loaded custom dark-theme logo: %s", name)
		}
	}

	sort.Slice(assets.CSSFiles, func(i, j int) bool {
		return assets.CSSFiles[i].Name < assets.CSSFiles[j].Name
	})

	return assets, nil
}

// matchesPrefix checks if the filename (without extension) equals the given prefix.
func matchesPrefix(lowerName, prefix string) bool {
	ext := filepath.Ext(lowerName)
	base := strings.TrimSuffix(lowerName, ext)
	return base == prefix
}

func loadCSSFile(assets *BrandingAssets, name, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading CSS file %q: %w", name, err)
	}
	if len(data) > maxCSSSize {
		return fmt.Errorf("CSS file %q exceeds maximum size of %d bytes", name, maxCSSSize)
	}
	assets.CSSFiles = append(assets.CSSFiles, BrandingCSS{
		Name:    name,
		Content: data,
	})
	return nil
}

func loadImageFile(bytesOut *[]byte, mimeOut, pathOut *string, name, path string, maxSize int) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(data) > maxSize {
		return fmt.Errorf("file exceeds maximum size of %d bytes", maxSize)
	}
	*bytesOut = data
	*mimeOut = mime.TypeByExtension(filepath.Ext(name))
	if *mimeOut == "" {
		*mimeOut = "application/octet-stream"
	}
	*pathOut = "/auth/branding/" + name
	return nil
}

// BrandingCSSPaths returns the URL paths for all loaded CSS files.
func (a *BrandingAssets) BrandingCSSPaths() []string {
	paths := make([]string, len(a.CSSFiles))
	for i, css := range a.CSSFiles {
		paths[i] = "/auth/branding/" + css.Name
	}
	return paths
}

// GetCSSByName returns the CSS content for a specific filename, or nil if not found.
func (a *BrandingAssets) GetCSSByName(name string) []byte {
	for _, css := range a.CSSFiles {
		if css.Name == name {
			return css.Content
		}
	}
	return nil
}
