package main

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func pvWithAffinity(key string, values ...string) *v1.PersistentVolume {
	return &v1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "pv"},
		Spec: v1.PersistentVolumeSpec{
			PersistentVolumeSource: v1.PersistentVolumeSource{
				HostPath: &v1.HostPathVolumeSource{Path: "/opt/local-path-provisioner/pv"},
			},
			NodeAffinity: &v1.VolumeNodeAffinity{
				Required: &v1.NodeSelector{
					NodeSelectorTerms: []v1.NodeSelectorTerm{
						{
							MatchExpressions: []v1.NodeSelectorRequirement{
								{Key: key, Operator: v1.NodeSelectorOpIn, Values: values},
							},
						},
					},
				},
			},
		},
	}
}

func TestNodeAffinityFor(t *testing.T) {
	t.Parallel()

	const customKey = "example.com/stable-id"

	tests := map[string]struct {
		labels      map[string]string
		affinityKey string
		wantValue   string
		wantErr     string
	}{
		"default key uses the hostname label": {
			labels:      map[string]string{DefaultNodeAffinityKey: "host-1"},
			affinityKey: DefaultNodeAffinityKey,
			wantValue:   "host-1",
		},
		"default key falls back to the node name": {
			affinityKey: DefaultNodeAffinityKey,
			wantValue:   "node-1",
		},
		"custom key uses the custom label": {
			labels:      map[string]string{DefaultNodeAffinityKey: "host-1", customKey: "stable-1"},
			affinityKey: customKey,
			wantValue:   "stable-1",
		},
		"custom key missing on the node is rejected": {
			labels:      map[string]string{DefaultNodeAffinityKey: "host-1"},
			affinityKey: customKey,
			wantErr:     "node node-1 has no example.com/stable-id label",
		},
		"custom key with an empty value is rejected": {
			labels:      map[string]string{customKey: ""},
			affinityKey: customKey,
			wantErr:     "node node-1 has no example.com/stable-id label",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			node := &v1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1", Labels: tt.labels}}
			nodeAffinity, err := nodeAffinityFor(node, tt.affinityKey)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)

			key, value, err := nodeAffinityTermFromPV(&v1.PersistentVolume{
				Spec: v1.PersistentVolumeSpec{NodeAffinity: nodeAffinity},
			})
			require.NoError(t, err)
			require.Equal(t, tt.affinityKey, key)
			require.Equal(t, tt.wantValue, value)
		})
	}
}

func TestNodeAffinityTermFromPV(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		pv        *v1.PersistentVolume
		wantKey   string
		wantValue string
		wantErr   string
	}{
		"single In expression": {
			pv:        pvWithAffinity("example.com/stable-id", "stable-1"),
			wantKey:   "example.com/stable-id",
			wantValue: "stable-1",
		},
		"no node affinity": {
			pv:      &v1.PersistentVolume{},
			wantErr: "no NodeAffinity set",
		},
		"no required node affinity": {
			pv:      &v1.PersistentVolume{Spec: v1.PersistentVolumeSpec{NodeAffinity: &v1.VolumeNodeAffinity{}}},
			wantErr: "no NodeAffinity.Required set",
		},
		"multiple values": {
			pv:      pvWithAffinity(DefaultNodeAffinityKey, "node-1", "node-2"),
			wantErr: "multiple values for the node affinity",
		},
		"no In expression": {
			pv: func() *v1.PersistentVolume {
				pv := pvWithAffinity(DefaultNodeAffinityKey)
				pv.Spec.NodeAffinity.Required.NodeSelectorTerms[0].MatchExpressions[0].Operator = v1.NodeSelectorOpExists
				return pv
			}(),
			wantErr: "cannot find affinited node",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			key, value, err := nodeAffinityTermFromPV(tt.pv)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantKey, key)
			require.Equal(t, tt.wantValue, value)
		})
	}
}

func TestNodeForAffinity(t *testing.T) {
	t.Parallel()

	node := func(name string) v1.Node {
		return v1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}}
	}

	tests := map[string]struct {
		matching      []v1.Node
		provisionedOn string
		want          string
		wantErr       string
	}{
		"single matching node": {
			matching:      []v1.Node{node("node-2")},
			provisionedOn: "node-1",
			want:          "node-2",
		},
		"no matching node falls back to the provisioning node": {
			provisionedOn: "node-1",
			want:          "node-1",
		},
		"no matching node and no provisioning node": {
			wantErr: "no node matches",
		},
		"multiple matching nodes": {
			matching:      []v1.Node{node("node-1"), node("node-2")},
			provisionedOn: "node-1",
			wantErr:       "2 nodes match",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := nodeForAffinity(tt.matching, tt.provisionedOn)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestGetPathAndNodeForPV(t *testing.T) {
	t.Parallel()

	localFS := &StorageClassConfig{
		NodePathMap: map[string]*NodePathMap{
			NodeDefaultNonListedNodes: {Paths: map[string]struct{}{"/opt/local-path-provisioner": {}}},
		},
	}
	sharedFS := &StorageClassConfig{SharedFileSystemPath: "/shared"}

	tests := map[string]struct {
		pv       *v1.PersistentVolume
		cfg      *StorageClassConfig
		wantPath string
		wantNode string
		wantErr  string
	}{
		"hostPath volume with default key": {
			pv:       pvWithAffinity(DefaultNodeAffinityKey, "node-1"),
			cfg:      localFS,
			wantPath: "/opt/local-path-provisioner/pv",
			wantNode: "node-1",
		},
		"local volume with default key": {
			pv: func() *v1.PersistentVolume {
				pv := pvWithAffinity(DefaultNodeAffinityKey, "node-1")
				pv.Spec.PersistentVolumeSource = v1.PersistentVolumeSource{
					Local: &v1.LocalVolumeSource{Path: "/opt/local-path-provisioner/pv"},
				}
				return pv
			}(),
			cfg:      localFS,
			wantPath: "/opt/local-path-provisioner/pv",
			wantNode: "node-1",
		},
		"shared filesystem has no node": {
			pv:       &v1.PersistentVolume{Spec: pvWithAffinity(DefaultNodeAffinityKey, "node-1").Spec},
			cfg:      sharedFS,
			wantPath: "/opt/local-path-provisioner/pv",
		},
		"no path set": {
			pv: func() *v1.PersistentVolume {
				pv := pvWithAffinity(DefaultNodeAffinityKey, "node-1")
				pv.Spec.PersistentVolumeSource = v1.PersistentVolumeSource{}
				return pv
			}(),
			cfg:     localFS,
			wantErr: "no path set",
		},
		"no node affinity": {
			pv: func() *v1.PersistentVolume {
				pv := pvWithAffinity(DefaultNodeAffinityKey, "node-1")
				pv.Spec.NodeAffinity = nil
				return pv
			}(),
			cfg:     localFS,
			wantErr: "no NodeAffinity set",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			p := &LocalPathProvisioner{config: &Config{}, configMutex: &sync.RWMutex{}}
			path, node, err := p.getPathAndNodeForPV(tt.pv, tt.cfg)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantPath, path)
			require.Equal(t, tt.wantNode, node)
		})
	}
}
