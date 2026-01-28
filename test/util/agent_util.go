package util

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/flightctl/flightctl/api/core/v1beta1"
)

func CreateInlineApplicationSpec(content string, path string) v1beta1.InlineApplicationProviderSpec {
	return v1beta1.InlineApplicationProviderSpec{
		Inline: []v1beta1.ApplicationContent{
			{
				Content: &content,
				Path:    path,
			},
		},
	}
}

type InlineContent struct {
	Path    string
	Content string
}

func BuildInlineAppSpec(appName string, appType v1beta1.AppType, contents []InlineContent) (v1beta1.ApplicationProviderSpec, error) {
	inline := make([]v1beta1.ApplicationContent, 0, len(contents))
	for _, c := range contents {
		content := c.Content
		inline = append(inline, v1beta1.ApplicationContent{
			Path:    c.Path,
			Content: &content,
		})
	}
	inlineSpec := v1beta1.InlineApplicationProviderSpec{Inline: inline}
	var appSpec v1beta1.ApplicationProviderSpec
	if err := appSpec.FromInlineApplicationProviderSpec(inlineSpec); err != nil {
		return v1beta1.ApplicationProviderSpec{}, err
	}
	appSpec.Name = &appName
	appSpec.AppType = appType
	return appSpec, nil
}

func BuildImageAppSpec(appName string, appType v1beta1.AppType, image string) (v1beta1.ApplicationProviderSpec, error) {
	imageSpec := v1beta1.ImageApplicationProviderSpec{Image: image}
	var appSpec v1beta1.ApplicationProviderSpec
	if err := appSpec.FromImageApplicationProviderSpec(imageSpec); err != nil {
		return v1beta1.ApplicationProviderSpec{}, err
	}
	appSpec.Name = &appName
	appSpec.AppType = appType
	return appSpec, nil
}

func UpdateDeviceApplicationFromInline(device *v1beta1.Device, inlineAppName string, inlineApp v1beta1.InlineApplicationProviderSpec) error {
	if device == nil || device.Spec == nil || device.Spec.Applications == nil || len(*device.Spec.Applications) == 0 {
		return fmt.Errorf("device spec applications are not set for %s", inlineAppName)
	}
	for i, app := range *device.Spec.Applications {
		if app.Name != nil && *app.Name == inlineAppName {
			err := (*device.Spec.Applications)[i].FromInlineApplicationProviderSpec(inlineApp)
			if err != nil {
				return fmt.Errorf("failed to update application %s from inline spec: %w", inlineAppName, err)
			}
			return nil
		}
	}
	return fmt.Errorf("application %s not found in device spec", inlineAppName)
}

func WriteHostFile(path string, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}

func WriteEvidence(dir, filename, command, output string, err error) error {
	var b strings.Builder
	b.WriteString("$ ")
	b.WriteString(command)
	b.WriteString("\n")
	if err != nil {
		b.WriteString("error: ")
		b.WriteString(err.Error())
		b.WriteString("\n")
	}
	b.WriteString(output)
	return os.WriteFile(filepath.Join(dir, filename), []byte(b.String()), 0o600)
}

func UniqueTempYAMLPath(baseName, testID string) string {
	return fmt.Sprintf("/tmp/%s-%s.yaml", baseName, testID)
}
