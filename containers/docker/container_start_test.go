package docker

import (
	"context"
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/go-connections/nat"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	options "github.com/openconfig/containerz/containers"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	cpb "github.com/openconfig/gnoi/containerz"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

type fakeStartingDocker struct {
	fakeDocker
	summaries []image.Summary
	cnts      []types.Container

	Ports       nat.PortSet
	Env         []string
	Volumes     []mount.Mount
	ContainerID string
	User        string
	Policy      container.RestartPolicy
	CapAdd      []string
	CapDel      []string
	Network     string
	Labels      map[string]string
	Devices     []container.DeviceMapping
	Cmd         []string

	CPU        int64
	HardMemory int64
	SoftMemory int64
}

func (f *fakeStartingDocker) ContainerCreate(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error) {
	f.Ports = config.ExposedPorts
	f.Cmd = config.Cmd
	f.Env = config.Env
	f.Volumes = hostConfig.Mounts
	f.User = config.User
	f.Policy = hostConfig.RestartPolicy
	f.CapAdd = hostConfig.CapAdd
	f.CapDel = hostConfig.CapDrop
	f.Labels = config.Labels
	f.CPU = hostConfig.Resources.NanoCPUs
	f.HardMemory = hostConfig.Resources.Memory
	f.SoftMemory = hostConfig.Resources.MemoryReservation
	f.Devices = hostConfig.Resources.Devices
	// If this is not out default, remember it.
	if !hostConfig.NetworkMode.IsHost() {
		f.Network = string(hostConfig.NetworkMode)
	}

	return container.CreateResponse{
		ID: containerName,
	}, nil
}

func (f *fakeStartingDocker) ContainerStart(ctx context.Context, container string, options container.StartOptions) error {
	f.ContainerID = container
	return nil
}

func (f fakeStartingDocker) ImageList(ctx context.Context, options image.ListOptions) ([]image.Summary, error) {
	return f.summaries, nil
}

func (f fakeStartingDocker) ContainerList(ctx context.Context, options container.ListOptions) ([]types.Container, error) {
	return f.cnts, nil
}

func TestContainerStart(t *testing.T) {
	tests := []struct {
		name        string
		inOpts      []options.Option
		inImage     string
		inTag       string
		inCmd       string
		inSummaries []image.Summary
		inCnts      []types.Container
		wantState   *fakeStartingDocker
		wantErr     error
	}{
		{
			name:    "no-such-image",
			inImage: "no-such-image",
			inTag:   "no-such-tag",
			wantErr: status.Error(codes.NotFound, "image no-such-image:no-such-tag not found"),
		},
		{
			name:    "container-with-instance-name-exists",
			inImage: "my-image",
			inTag:   "my-tag",
			inCmd:   "my-cmd",
			inSummaries: []image.Summary{
				{
					RepoTags: []string{"my-image:my-tag"},
				},
			},
			inCnts: []types.Container{
				{
					Names: []string{"/my-container"},
				},
			},
			inOpts:  []options.Option{options.WithInstanceName("my-container")},
			wantErr: status.Errorf(codes.AlreadyExists, "instance name my-container already in use"),
		},
		{
			name:    "container-with-empty-user",
			inImage: "my-image",
			inTag:   "my-tag",
			inCmd:   "my-cmd",
			inSummaries: []image.Summary{
				{
					RepoTags: []string{"my-image:my-tag"},
				},
			},
			inCnts: []types.Container{
				{
					Names: []string{"/my-container"},
				},
			},
			inOpts: []options.Option{
				options.WithRunAs(&cpb.StartContainerRequest_RunAs{}),
			},
			wantErr: status.Errorf(codes.FailedPrecondition, "user can not be empty in RunAs option"),
		},
		{
			name:    "bad-container-trying-to-reuse-port",
			inImage: "my-image",
			inTag:   "my-tag",
			inCmd:   "my-cmd",
			inSummaries: []image.Summary{
				{
					RepoTags: []string{"my-image:my-tag"},
				},
			},
			inCnts: []types.Container{
				{
					Ports: []types.Port{
						{
							PublicPort: 1,
						},
					},
				},
			},
			inOpts:  []options.Option{options.WithInstanceName("my-container"), options.WithPorts(map[uint32]uint32{1: 1})},
			wantErr: status.Errorf(codes.Unavailable, "port 1 already in use"),
		},
		{
			name:    "container-with-ports",
			inImage: "my-image",
			inTag:   "my-tag",
			inCmd:   "my-cmd",
			inSummaries: []image.Summary{
				{
					RepoTags: []string{"my-image:my-tag"},
				},
			},
			inOpts: []options.Option{options.WithInstanceName("my-container"), options.WithPorts(map[uint32]uint32{1: 1})},
			wantState: &fakeStartingDocker{
				Cmd:         []string{"my-cmd"},
				Ports:       nat.PortSet{"1/tcp": struct{}{}},
				ContainerID: "my-container",
				Volumes:     []mount.Mount{},
			},
		},
		{
			name:    "container-with-user-but-no-group",
			inImage: "my-image",
			inTag:   "my-tag",
			inCmd:   "my-cmd",
			inSummaries: []image.Summary{
				{
					RepoTags: []string{"my-image:my-tag"},
				},
			},
			inCnts: []types.Container{
				{
					Names: []string{"/my-container"},
				},
			},
			inOpts: []options.Option{
				options.WithRunAs(&cpb.StartContainerRequest_RunAs{
					User: "my-user",
				}),
			},
			wantState: &fakeStartingDocker{
				Cmd:  []string{"my-cmd"},
				User: "my-user",
			},
		},
		{
			name:    "container-with-user-and-group",
			inImage: "my-image",
			inTag:   "my-tag",
			inCmd:   "my-cmd",
			inSummaries: []image.Summary{
				{
					RepoTags: []string{"my-image:my-tag"},
				},
			},
			inCnts: []types.Container{
				{
					Names: []string{"/my-container"},
				},
			},
			inOpts: []options.Option{
				options.WithRunAs(&cpb.StartContainerRequest_RunAs{
					User:  "my-user",
					Group: "my-group",
				}),
			},
			wantState: &fakeStartingDocker{
				Cmd:  []string{"my-cmd"},
				User: "my-user:my-group",
			},
		},
		{
			name:    "container-with-retry-policy-and-attempts",
			inImage: "my-image",
			inTag:   "my-tag",
			inCmd:   "my-cmd",
			inSummaries: []image.Summary{
				{
					RepoTags: []string{"my-image:my-tag"},
				},
			},
			inCnts: []types.Container{
				{
					Names: []string{"/my-container"},
				},
			},
			inOpts: []options.Option{
				options.WithRestartPolicy(&cpb.StartContainerRequest_Restart{
					Policy:   cpb.StartContainerRequest_Restart_ON_FAILURE,
					Attempts: 3,
				}),
			},
			wantState: &fakeStartingDocker{
				Cmd: []string{"my-cmd"},
				Policy: container.RestartPolicy{
					Name:              "on-failure",
					MaximumRetryCount: 3,
				},
			},
		},
		{
			name:    "container-with-capabilities",
			inImage: "my-image",
			inTag:   "my-tag",
			inCmd:   "my-cmd",
			inSummaries: []image.Summary{
				{
					RepoTags: []string{"my-image:my-tag"},
				},
			},
			inCnts: []types.Container{
				{
					Names: []string{"/my-container"},
				},
			},
			inOpts: []options.Option{
				options.WithCapabilities(&cpb.StartContainerRequest_Capabilities{
					Add:    []string{"my-add-capability"},
					Remove: []string{"my-remove-capability"},
				}),
			},
			wantState: &fakeStartingDocker{
				Cmd:    []string{"my-cmd"},
				CapAdd: []string{"my-add-capability"},
				CapDel: []string{"my-remove-capability"},
			},
		},
		{
			name:    "container-with-network",
			inImage: "my-image",
			inTag:   "my-tag",
			inCmd:   "my-cmd",
			inSummaries: []image.Summary{
				{
					RepoTags: []string{"my-image:my-tag"},
				},
			},
			inCnts: []types.Container{
				{
					Names: []string{"/my-container"},
				},
			},
			inOpts: []options.Option{
				options.WithNetwork("my-network"),
			},
			wantState: &fakeStartingDocker{
				Cmd:     []string{"my-cmd"},
				Network: "my-network",
			},
		},
		{
			name:    "container-with-labels",
			inImage: "my-image",
			inTag:   "my-tag",
			inCmd:   "my-cmd",
			inSummaries: []image.Summary{
				{
					RepoTags: []string{"my-image:my-tag"},
				},
			},
			inCnts: []types.Container{
				{
					Names: []string{"/my-container"},
				},
			},
			inOpts: []options.Option{
				options.WithLabels(map[string]string{"label": "value"}),
			},
			wantState: &fakeStartingDocker{
				Cmd:    []string{"my-cmd"},
				Labels: map[string]string{"label": "value"},
			},
		},
		{
			name:    "container-with-env-and-port",
			inImage: "my-image",
			inTag:   "my-tag",
			inCmd:   "my-cmd",
			inSummaries: []image.Summary{
				{
					RepoTags: []string{"my-image:my-tag"},
				},
			},
			inOpts: []options.Option{options.WithInstanceName("my-container"), options.WithPorts(map[uint32]uint32{1: 1}), options.WithEnv(map[string]string{"AA": "BB"})},
			wantState: &fakeStartingDocker{
				Cmd:         []string{"my-cmd"},
				Ports:       nat.PortSet{"1/tcp": struct{}{}},
				Env:         []string{"AA=BB"},
				ContainerID: "my-container",
				Volumes:     []mount.Mount{},
			},
		},
		{
			name:    "container-with-env-and-port-and-volumes-and-devices",
			inImage: "my-image",
			inTag:   "my-tag",
			inCmd:   "my-cmd",
			inSummaries: []image.Summary{
				{
					RepoTags: []string{"my-image:my-tag"},
				},
			},
			inOpts: []options.Option{
				options.WithInstanceName("my-container"),
				options.WithPorts(map[uint32]uint32{1: 1}),
				options.WithEnv(map[string]string{"AA": "BB"}),
				options.WithVolumes([]*cpb.Volume{
					{
						Name:       "my-volume",
						MountPoint: "/tmp",
					},
				}),
				options.WithDevices([]*cpb.Device{
					{
						SrcPath:     "/dev/my-device",
						DstPath:     "/dev/my-device",
						Permissions: []cpb.Device_Permission{cpb.Device_READ, cpb.Device_WRITE, cpb.Device_MKNOD},
					},
				}),
			},
			wantState: &fakeStartingDocker{
				Cmd:         []string{"my-cmd"},
				Ports:       nat.PortSet{"1/tcp": struct{}{}},
				Env:         []string{"AA=BB"},
				ContainerID: "my-container",
				Volumes: []mount.Mount{
					{
						Type:   "volume",
						Source: "my-volume",
						Target: "/tmp",
					},
				},
				Devices: []container.DeviceMapping{
					{
						PathOnHost:        "/dev/my-device",
						PathInContainer:   "/dev/my-device",
						CgroupPermissions: "rwm",
					},
				},
			},
		},
		{
			name:    "container-with-cpu-soft-and-hard-memory",
			inImage: "my-image",
			inTag:   "my-tag",
			inCmd:   "my-cmd",
			inSummaries: []image.Summary{
				{
					RepoTags: []string{"my-image:my-tag"},
				},
			},
			inOpts: []options.Option{
				options.WithInstanceName("my-container"),
				options.WithCPUs(1.0),
				options.WithSoftLimit(1000),
				options.WithHardLimit(2000),
			},
			wantState: &fakeStartingDocker{
				Cmd:         []string{"my-cmd"},
				ContainerID: "my-container",
				CPU:         1000000000,
				HardMemory:  2000,
				SoftMemory:  1000,
			},
		},
		{
			name:    "container-with-cmd",
			inImage: "my-image",
			inTag:   "my-tag",
			inCmd:   `sleep 1000`,
			inSummaries: []image.Summary{{
				RepoTags: []string{"my-image:my-tag"},
			}},
			wantState: &fakeStartingDocker{
				Cmd: []string{"sleep", "1000"},
			},
		},
		{
			name:    "container-with-sh-cmd",
			inImage: "my-image",
			inTag:   "my-tag",
			inCmd:   `sh -c "echo 2"`,
			inSummaries: []image.Summary{{
				RepoTags: []string{"my-image:my-tag"},
			}},
			wantState: &fakeStartingDocker{
				Cmd: []string{"sh", "-c", "echo 2"},
			},
		},
		{
			name:    "container-with-quoted-cmd",
			inImage: "my-image",
			inTag:   "my-tag",
			inCmd:   `echo 'echo "quoted"'`,
			inSummaries: []image.Summary{{
				RepoTags: []string{"my-image:my-tag"},
			}},
			wantState: &fakeStartingDocker{
				Cmd: []string{"echo", `echo "quoted"`},
			},
		},
		{
			name:    "container-with-no-cmd",
			inImage: "my-image",
			inTag:   "my-tag",
			inSummaries: []image.Summary{{
				RepoTags: []string{"my-image:my-tag"},
			}},
			wantState: &fakeStartingDocker{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fsd := &fakeStartingDocker{
				summaries: tc.inSummaries,
				cnts:      tc.inCnts,
			}
			mgr := New(fsd)

			if _, err := mgr.ContainerStart(context.Background(), tc.inImage, tc.inTag, tc.inCmd, tc.inOpts...); err != nil {
				if tc.wantErr != nil {
					if diff := cmp.Diff(tc.wantErr, err, cmpopts.EquateErrors()); diff != "" {
						t.Errorf("ContainerStart(%q, %q, %q, %+v) returned unexpected error(-want, got):\n %s", tc.inImage, tc.inTag, tc.inCmd, tc.inOpts, diff)
					}
					return
				}
				t.Errorf("ContainerStart(%q, %q, %q, %+v) returned error: %v", tc.inImage, tc.inTag, tc.inCmd, tc.inOpts, err)
			}

			if tc.wantState != nil {
				if diff := cmp.Diff(tc.wantState, fsd, cmpopts.IgnoreUnexported(fakeStartingDocker{}), cmpopts.EquateEmpty()); diff != "" {
					t.Errorf("ContainerStart(%q, %q, %q, %+v) returned diff(-want, +got):\n%s", tc.inImage, tc.inTag, tc.inCmd, tc.inOpts, diff)
				}
			}
		})
	}
}
