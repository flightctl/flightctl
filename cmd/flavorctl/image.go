package main

import (
	"fmt"
	"sort"

	"github.com/flightctl/flightctl/internal/pkg/flavors"
	"github.com/spf13/cobra"
)

func NewImageCommand(flavorsFile, overrideFile *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "image",
		Short: "Query image information",
		Long:  "Query image information for flavors",
	}

	cmd.AddCommand(NewImageGetCommand(flavorsFile, overrideFile))
	cmd.AddCommand(NewImageListCommand(flavorsFile, overrideFile))

	return cmd
}

func NewImageGetCommand(flavorsFile, overrideFile *string) *cobra.Command {
	var buildImage bool

	cmd := &cobra.Command{
		Use:   "get FLAVOR_NAME IMAGE_NAME",
		Short: "Get image reference for a service",
		Long:  "Get the full image reference (image:tag) for a specific service in a flavor",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			flavorName := args[0]
			imageName := args[1]

			var override string
			if overrideFile != nil && *overrideFile != "" {
				override = *overrideFile
			}

			flavorsPath := ""
			if flavorsFile != nil {
				flavorsPath = *flavorsFile
			}
			flavor, err := flavors.GetFlavor(flavorName, flavorsPath, override)
			if err != nil {
				return err
			}

			if buildImage {
				imageRef, err := flavor.GetBuildImageReference(imageName)
				if err != nil {
					return err
				}
				fmt.Println(imageRef)
			} else {
				image, tag, found := flavor.GetFlavorImageTag(imageName)
				if !found {
					return fmt.Errorf("image %s not found in flavor %s", imageName, flavorName)
				}

				if tag != "" {
					fmt.Printf("%s:%s\n", image, tag)
				} else {
					fmt.Println(image)
				}
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&buildImage, "build", false,
		"Query build image instead of service image")

	return cmd
}

func NewImageListCommand(flavorsFile, overrideFile *string) *cobra.Command {
	var buildImages bool

	cmd := &cobra.Command{
		Use:   "list FLAVOR_NAME",
		Short: "List all images in a flavor",
		Long:  "List all available images in a specific flavor",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			flavorName := args[0]

			var override string
			if overrideFile != nil && *overrideFile != "" {
				override = *overrideFile
			}

			flavorsPath := ""
			if flavorsFile != nil {
				flavorsPath = *flavorsFile
			}
			flavor, err := flavors.GetFlavor(flavorName, flavorsPath, override)
			if err != nil {
				return err
			}

			if buildImages {
				fmt.Println("Build images:")
				buildImageNames := []string{"goToolset", "ubiMinimal", "base", "baseMinimal"}
				for _, name := range buildImageNames {
					if ref, err := flavor.GetBuildImageReference(name); err == nil {
						fmt.Printf("  %-12s %s\n", name+":", ref)
					}
				}
			} else {
				if len(flavor.Images) == 0 {
					fmt.Println("No service images found")
					return nil
				}

				fmt.Println("Service images:")

				// Sort image names for consistent output
				imageNames := make([]string, 0, len(flavor.Images))
				for name := range flavor.Images {
					imageNames = append(imageNames, name)
				}
				sort.Strings(imageNames)

				for _, name := range imageNames {
					imageConfig := flavor.Images[name]
					if imageConfig.Tag != "" {
						fmt.Printf("  %-20s %s:%s\n", name+":", imageConfig.Image, imageConfig.Tag)
					} else {
						fmt.Printf("  %-20s %s\n", name+":", imageConfig.Image)
					}
				}
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&buildImages, "build", false,
		"List build images instead of service images")

	return cmd
}
