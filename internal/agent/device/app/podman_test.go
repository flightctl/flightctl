package app

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestPodmanList(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name          string
		containerIds  []string
		matchPatterns []string
		appCount      int
		result        []string
	}{
		{
			name:          "happy path",
			containerIds:  []string{"id1", "id2"},
			matchPatterns: []string{"myfirstname", "agreatname"},
			appCount:      2,
			result:        []string{podmanListResult, podmanInspectResult},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Second)
			defer cancel()

			ctrl := gomock.NewController(t)
			execMock := executer.NewMockExecuter(ctrl)
			defer ctrl.Finish()

			// list containers
			listArgs := []string{"ps", "-a", "--format", "json"}
			for _, pattern := range tt.matchPatterns {
				listArgs = append(listArgs, "--filter")
				listArgs = append(listArgs, fmt.Sprintf("name=%s", pattern))
			}
			execMock.EXPECT().ExecuteWithContext(ctx, PodmanCmd, listArgs).Return(tt.result[0], "", 0)

			// inspect containers
			for _, id := range tt.containerIds {
				statusArgs := []string{
					"inspect",
					id,
				}
				execMock.EXPECT().ExecuteWithContext(ctx, PodmanCmd, statusArgs).Return(tt.result[1], "", 0)
			}

			podman := NewPodmanClient(execMock)
			apps, err := podman.List(ctx, tt.matchPatterns...)
			require.NoError(err)
			if len(apps) > 0 {
				require.Len(apps, tt.appCount)
			}
		})
	}
}

func TestPodmanGetStatus(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name         string
		containerIds []string
		result       []string
	}{
		{
			name:         "success",
			containerIds: []string{"id1", "id2"},
			result:       []string{podmanInspectResult},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Second)
			defer cancel()

			ctrl := gomock.NewController(t)
			execMock := executer.NewMockExecuter(ctrl)
			defer ctrl.Finish()

			for _, id := range tt.containerIds {
				statusArgs := []string{
					"inspect",
					id,
				}
				execMock.EXPECT().ExecuteWithContext(ctx, PodmanCmd, statusArgs).Return(tt.result[0], "", 0)
			}

			podman := NewPodmanClient(execMock)
			for _, id := range tt.containerIds {
				app, err := podman.GetStatus(ctx, id)
				require.NoError(err)
				require.Equal(id, *app.Id)
				require.Equal(v1alpha1.ApplicationStateRunning, *app.State)
				require.Equal(1, *app.Restarts)
				require.NotEmpty(*app.Name)
			}
		})
	}
}

const (
	podmanListResult = `[
  {
    "AutoRemove": true,
    "Command": [
      "command"
    ],
    "CreatedAt": "40 minutes ago",
    "CIDFile": "",
    "Exited": false,
    "ExitedAt": -62135596800,
    "ExitCode": 0,
    "Id": "id1",
    "Image": "quay.io/image1:latest",
    "ImageID": "b22b91a96569c182755b01c5ab342d7194f825984fa293cebfd1b1c8c383252b",
    "IsInfra": false,
    "Labels": {
      "description": "A lovely description.",
      "version": "1"
    },
    "Mounts": [
      "/foo"
    ],
    "Names": [
      "myfirstname",
	  "myothername"
    ],
    "Namespaces": {

    },
    "Networks": [],
    "Pid": 1136940,
    "Pod": "",
    "PodName": "",
    "Ports": [
      {
        "host_ip": "127.0.0.1",
        "container_port": 1234,
        "host_port": 1234,
        "range": 1,
        "protocol": "tcp"
      }
    ],
    "Restarts": 0,
    "Size": null,
    "StartedAt": 1706178662,
    "State": "running",
    "Status": "Up 40 minutes",
    "Created": 1706178662
  },
  {
    "AutoRemove": true,
    "Command": [
      "mycommand"
    ],
    "CreatedAt": "46 minutes ago",
    "CIDFile": "",
    "Exited": false,
    "ExitedAt": -62135596800,
    "ExitCode": 0,
    "Id": "id2",
    "Image": "quay.io/image2:latest",
    "ImageID": "b22b91a96569c182755b01c5ab342d7194f825984fa293cebfd1b1c8c383252c",
    "IsInfra": false,
    "Labels": {
      "description": "This describes it perfectly.",
      "version": "2"
    },
    "Mounts": [
      "/bar"
    ],
    "Names": [
      "agreatname"
    ],
    "Namespaces": {

    },
    "Networks": [],
    "Pid": 1136940,
    "Pod": "",
    "PodName": "",
    "Ports": [
      {
        "host_ip": "127.0.0.1",
        "container_port": 4444,
        "host_port": 4444,
        "range": 1,
        "protocol": "tcp"
      }
    ],
    "Restarts": 0,
    "Size": null,
    "StartedAt": 1706178662,
    "State": "paused",
    "Status": "Paused",
    "Created": 1706178662
  }
]`

	podmanInspectResult = `[
     {
          "Id": "2f03be24955338ab7d3c4ec5a7d4ea8882ffec23ef7b156a149566e95b1876a6",
          "Created": "2024-05-31T13:10:16.832367424Z",
          "Path": "machine-config-daemon",
          "Args": [
               "firstboot-complete-machineconfig"
          ],
        "State": {
               "OciVersion": "1.1.0-rc.1",
               "Status": "running",
               "Running": true,
               "Paused": false,
               "Restarting": false,
               "OOMKilled": false,
               "Dead": false,
               "Pid": 2285559,
               "ConmonPid": 2285554,
               "ExitCode": 0,
               "Error": "",
               "StartedAt": "2024-06-05T20:18:03.552644599Z",
               "FinishedAt": "0001-01-01T00:00:00Z",
               "Health": {
                    "Status": "",
                    "FailingStreak": 0,
                    "Log": null
               },
               "CgroupPath": "/machine.slice/libpod-55ea3863351726184e26ca7418f577d04c96dac0009e1f088f6c0c7a1dc2ed7c.scope",
               "CheckpointedAt": "0001-01-01T00:00:00Z",
               "RestoredAt": "0001-01-01T00:00:00Z"
          }, 
          "Image": "0a1d3e6e807af162dd090ddf7265d31a01e9b9b08fd4d91af13f1dd218e791bc",
          "ImageDigest": "sha256:86477d2e586c857d9c5ec68d0426f763b2cdcded403bc72cf5ced20f2ce00f89",
          "ImageName": "quay.io/openshift-release-dev/ocp-v4.0-art-dev@sha256:86477d2e586c857d9c5ec68d0426f763b2cdcded403bc72cf5ced20f2ce00f89",
          "Rootfs": "",
          "Pod": "",
          "ResolvConfPath": "",
          "HostnamePath": "",
          "HostsPath": "",
          "StaticDir": "/var/lib/containers/storage/overlay-containers/2f03be24955338ab7d3c4ec5a7d4ea8882ffec23ef7b156a149566e95b1876a6/userdata",
          "OCIConfigPath": "/var/lib/containers/storage/overlay-containers/2f03be24955338ab7d3c4ec5a7d4ea8882ffec23ef7b156a149566e95b1876a6/userdata/config.json",
          "OCIRuntime": "crun",
          "ConmonPidFile": "/run/containers/storage/overlay-containers/2f03be24955338ab7d3c4ec5a7d4ea8882ffec23ef7b156a149566e95b1876a6/userdata/conmon.pid",
          "PidFile": "/run/containers/storage/overlay-containers/2f03be24955338ab7d3c4ec5a7d4ea8882ffec23ef7b156a149566e95b1876a6/userdata/pidfile",
          "Name": "recursing_lewin",
          "RestartCount": 1,
          "Driver": "overlay",
          "MountLabel": "system_u:object_r:container_file_t:s0:c1022,c1023",
          "ProcessLabel": "",
          "AppArmorProfile": "",
          "EffectiveCaps": [
               "CAP_AUDIT_CONTROL"
          ],
          "BoundingCaps": [
               "CAP_AUDIT_CONTROL"
          ],
          "ExecIDs": [],
          "GraphDriver": {
               "Name": "overlay",
               "Data": {
                    "LowerDir": "/var/lib/containers/storage/overlay/a3692f8af64a225b8f75e20821a66d568f1ffc33f3791f49536bbb2d049fa277/diff:/var/lib/containers/storage/overlay/61e1a3b2c9fb93b32bd533816453b21c14a47ca42c903bdbe1ff5412ce10c7ac/diff:/var/lib/containers/storage/overlay/2ba92635c2e37d1a5934c7f56b638f9f4d0967753af11320c540423d38f7b96c/diff:/var/lib/containers/storage/overlay/86c3c60d456d5a412337f501a5543c8c529257dafb7f9bbc70793afe5f59c938/diff:/var/lib/containers/storage/overlay/e2e51ecd22dcbc318fb317f20dff685c6d54755d60a80b12ed290658864d45fd/diff",
                    "UpperDir": "/var/lib/containers/storage/overlay/ff66cbad6b5d62bbb65863cfdb6c06bad6c34edc44199d6d69f1407eb85dba21/diff",
                    "WorkDir": "/var/lib/containers/storage/overlay/ff66cbad6b5d62bbb65863cfdb6c06bad6c34edc44199d6d69f1407eb85dba21/work"
               }
          },
          "Mounts": [
               {
                    "Type": "bind",
                    "Source": "/",
                    "Destination": "/rootfs",
                    "Driver": "",
                    "Mode": "",
                    "Options": [
                         "rbind"
                    ],
                    "RW": true,
                    "Propagation": "rprivate"
               }
          ],
          "Dependencies": [],
          "NetworkSettings": {
               "EndpointID": "",
               "Gateway": "",
               "IPAddress": "",
               "IPPrefixLen": 0,
               "IPv6Gateway": "",
               "GlobalIPv6Address": "",
               "GlobalIPv6PrefixLen": 0,
               "MacAddress": "",
               "Bridge": "",
               "SandboxID": "",
               "HairpinMode": false,
               "LinkLocalIPv6Address": "",
               "LinkLocalIPv6PrefixLen": 0,
               "Ports": {},
               "SandboxKey": ""
          },
          "Namespace": "",
          "IsInfra": false,
          "IsService": false,
          "Config": {
               "Hostname": "ip-10-0-54-155",
               "Domainname": "",
               "User": "",
               "AttachStdin": false,
               "AttachStdout": false,
               "AttachStderr": false,
               "Tty": false,
               "OpenStdin": false,
               "StdinOnce": false,
               "Env": [
                    "BUILD_VERSION=v4.15.0",
                    "OS_GIT_PATCH=0",
                    "OS_GIT_TREE_STATE=clean",
                    "SOURCE_DATE_EPOCH=1715692642",
                    "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
                    "SOURCE_GIT_URL=https://github.com/openshift/machine-config-operator",
                    "SOURCE_GIT_TREE_STATE=clean",
                    "__doozer_group=openshift-4.15",
                    "SOURCE_GIT_COMMIT=10694c7cc870f32bc7bf1888d1b8199f13119ff4",
                    "OS_GIT_MAJOR=4",
                    "container=oci",
                    "__doozer_version=v4.15.0",
                    "OS_GIT_MINOR=15",
                    "OS_GIT_COMMIT=10694c7",
                    "SOURCE_GIT_TAG=unreleased-master-2536-g10694c7cc",
                    "TERM=xterm",
                    "BUILD_RELEASE=202405222235.p0.g10694c7.assembly.stream.el8",
                    "GODEBUG=x509ignoreCN=0,madvdontneed=1",
                    "__doozer=merge",
                    "__doozer_key=ose-machine-config-operator",
                    "OS_GIT_VERSION=4.15.0-202405222235.p0.g10694c7.assembly.stream.el8-10694c7",
                    "HOME=/root",
                    "HOSTNAME=ip-10-0-54-155"
               ],
               "Cmd": [
                    "firstboot-complete-machineconfig"
               ],
               "Image": "quay.io/openshift-release-dev/ocp-v4.0-art-dev@sha256:86477d2e586c857d9c5ec68d0426f763b2cdcded403bc72cf5ced20f2ce00f89",
               "Volumes": null,
               "WorkingDir": "/",
               "Entrypoint": "machine-config-daemon",
               "OnBuild": null,
               "Labels": {
                    "License": "GPLv2+",
                    "architecture": "x86_64",
                    "build-date": "2024-05-22T23:50:21",
                    "com.redhat.build-host": "cpt-1001.osbs.prod.upshift.rdu2.redhat.com",
                    "com.redhat.component": "ose-machine-config-operator-container",
                    "com.redhat.license_terms": "https://www.redhat.com/agreements",
                    "description": "This is the base image from which all OpenShift Container Platform images inherit.",
                    "distribution-scope": "public",
                    "io.buildah.version": "1.29.0",
                    "io.k8s.description": "This is the base image from which all OpenShift Container Platform images inherit.",
                    "io.k8s.display-name": "OpenShift Container Platform RHEL 8 Base",
                    "io.openshift.build.commit.id": "10694c7cc870f32bc7bf1888d1b8199f13119ff4",
                    "io.openshift.build.commit.url": "https://github.com/openshift/machine-config-operator/commit/10694c7cc870f32bc7bf1888d1b8199f13119ff4",
                    "io.openshift.build.source-location": "https://github.com/openshift/machine-config-operator",
                    "io.openshift.expose-services": "",
                    "io.openshift.maintainer.component": "Machine Config Operator",
                    "io.openshift.maintainer.project": "OCPBUGS",
                    "io.openshift.release.operator": "true",
                    "io.openshift.tags": "openshift,base",
                    "maintainer": "Red Hat, Inc.",
                    "name": "openshift/ose-machine-config-operator",
                    "release": "202405222235.p0.g10694c7.assembly.stream.el8",
                    "summary": "Provides the latest release of the Red Hat Extended Life Base Image.",
                    "url": "https://access.redhat.com/containers/#/registry.access.redhat.com/openshift/ose-machine-config-operator/images/v4.15.0-202405222235.p0.g10694c7.assembly.stream.el8",
                    "vcs-ref": "d14ba5b33dda38f7adfeec5d738e7da79e999b1e",
                    "vcs-type": "git",
                    "vendor": "Red Hat, Inc.",
                    "version": "v4.15.0"
               },
               "Annotations": {
                    "io.container.manager": "libpod",
                    "io.podman.annotations.autoremove": "TRUE",
                    "io.podman.annotations.privileged": "TRUE",
                    "org.opencontainers.image.stopSignal": "15"
               },
               "StopSignal": 15,
               "HealthcheckOnFailureAction": "none",
               "CreateCommand": [
                    "/usr/bin/podman",
                    "run",
                    "--rm",
                    "--privileged",
                    "--pid=host",
                    "--net=host",
                    "-v",
                    "/:/rootfs",
                    "--entrypoint",
                    "machine-config-daemon",
                    "quay.io/openshift-release-dev/ocp-v4.0-art-dev@sha256:86477d2e586c857d9c5ec68d0426f763b2cdcded403bc72cf5ced20f2ce00f89",
                    "firstboot-complete-machineconfig"
               ],
               "Umask": "0022",
               "Timeout": 0,
               "StopTimeout": 10,
               "Passwd": true,
               "sdNotifyMode": "container"
          },
          "HostConfig": {
               "Binds": [
                    "/:/rootfs:rw,rprivate,rbind"
               ],
               "CgroupManager": "systemd",
               "CgroupMode": "private",
               "ContainerIDFile": "",
               "LogConfig": {
                    "Type": "journald",
                    "Config": null,
                    "Path": "",
                    "Tag": "",
                    "Size": "0B"
               },
               "NetworkMode": "host",
               "PortBindings": {},
               "RestartPolicy": {
                    "Name": "",
                    "MaximumRetryCount": 0
               },
               "AutoRemove": true,
               "VolumeDriver": "",
               "VolumesFrom": null,
               "CapAdd": [],
               "CapDrop": [],
               "Dns": [],
               "DnsOptions": [],
               "DnsSearch": [],
               "ExtraHosts": [],
               "GroupAdd": [],
               "IpcMode": "shareable",
               "Cgroup": "",
               "Cgroups": "default",
               "Links": null,
               "OomScoreAdj": 0,
               "PidMode": "host",
               "Privileged": true,
               "PublishAllPorts": false,
               "ReadonlyRootfs": false,
               "SecurityOpt": [],
               "Tmpfs": {},
               "UTSMode": "private",
               "UsernsMode": "",
               "ShmSize": 65536000,
               "Runtime": "oci",
               "ConsoleSize": [
                    0,
                    0
               ],
               "Isolation": "",
               "CpuShares": 0,
               "Memory": 0,
               "NanoCpus": 0,
               "CgroupParent": "",
               "BlkioWeight": 0,
               "BlkioWeightDevice": null,
               "BlkioDeviceReadBps": null,
               "BlkioDeviceWriteBps": null,
               "BlkioDeviceReadIOps": null,
               "BlkioDeviceWriteIOps": null,
               "CpuPeriod": 0,
               "CpuQuota": 0,
               "CpuRealtimePeriod": 0,
               "CpuRealtimeRuntime": 0,
               "CpusetCpus": "",
               "CpusetMems": "",
               "Devices": [],
               "DiskQuota": 0,
               "KernelMemory": 0,
               "MemoryReservation": 0,
               "MemorySwap": 0,
               "MemorySwappiness": 0,
               "OomKillDisable": false,
               "PidsLimit": 2048,
               "Ulimits": [
                    {
                         "Name": "RLIMIT_NOFILE",
                         "Soft": 1048576,
                         "Hard": 1048576
                    },
                    {
                         "Name": "RLIMIT_NPROC",
                         "Soft": 4194304,
                         "Hard": 4194304
                    }
               ],
               "CpuCount": 0,
               "CpuPercent": 0,
               "IOMaximumIOps": 0,
               "IOMaximumBandwidth": 0,
               "CgroupConf": null
          }
     }
]`
)
