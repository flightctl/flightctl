package tasks

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/go-git/go-billy/v5/memfs"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ConvertFileSystemToIgnition", func() {
	When("the path is a directory", func() {
		It("converts the directory to an ignition config with subdirectory and files", func() {
			mfs := memfs.New()
			_ = mfs.MkdirAll("/testDAta/etc/testdir", 0755)
			files := []string{"/testdir/file1", "/file2"}
			sort.Slice(files, func(i, j int) bool { return strings.ToLower(files[i]) < strings.ToLower(files[j]) })

			file1, _ := mfs.Create(filepath.Join("/testDAta", files[0]))
			_, _ = file1.Write([]byte("content1"))
			file2, _ := mfs.Create(filepath.Join("/testDAta", files[1]))
			_, _ = file2.Write([]byte("content2"))

			ignitionConfig, err := ConvertFileSystemToIgnition(mfs, "/testDAta")
			Expect(err).ToNot(HaveOccurred())
			Expect(ignitionConfig.Storage.Files).To(HaveLen(2))

			sort.Slice(ignitionConfig.Storage.Files, func(i, j int) bool {
				return strings.ToLower(ignitionConfig.Storage.Files[i].Path) < strings.ToLower(ignitionConfig.Storage.Files[j].Path)
			})
			Expect(ignitionConfig.Storage.Files[0].Path).To(Equal(filepath.Join("/etc/", files[0])))
			Expect(ignitionConfig.Storage.Files[1].Path).To(Equal(filepath.Join("/etc/", files[1])))
		})
	})

	When("the path is a file in non-slash folder", func() {
		It("converts the file to an ignition config", func() {
			mfs := memfs.New()
			path := "/somefolder/testfile"
			file, _ := mfs.Create(path)
			_, _ = file.Write([]byte("content"))

			ignitionConfig, err := ConvertFileSystemToIgnition(mfs, path)
			Expect(err).ToNot(HaveOccurred())
			Expect(ignitionConfig.Storage.Files).To(HaveLen(1))
			Expect(ignitionConfig.Storage.Files[0].Path).To(Equal("/etc/testfile"))
		})
	})

	When("the path is a file in / folder", func() {
		It("converts the file to an ignition config", func() {
			mfs := memfs.New()
			file, _ := mfs.Create("/testfile")
			_, _ = file.Write([]byte("content"))

			ignitionConfig, err := ConvertFileSystemToIgnition(mfs, "/testfile")
			Expect(err).ToNot(HaveOccurred())
			Expect(ignitionConfig.Storage.Files).To(HaveLen(1))
			Expect(ignitionConfig.Storage.Files[0].Path).To(Equal("/testfile"))
		})
	})

	When("the path does not exist", func() {
		It("returns an error", func() {
			mfs := memfs.New()

			_, err := ConvertFileSystemToIgnition(mfs, "/nonexistent")
			Expect(err).To(HaveOccurred())
		})
	})

	When("the path is empty", func() {
		It("returns an error", func() {
			mfs := memfs.New()

			_, err := ConvertFileSystemToIgnition(mfs, "")
			Expect(err).To(HaveOccurred())
		})
	})
})
