package runtime

import (
	"reflect"
	"testing"
)

func TestAppVolumeArgs(t *testing.T) {
	cases := []struct {
		name    string
		volumes []VolumeMount
		want    []string
	}{
		{
			name:    "sem volumes não gera args",
			volumes: nil,
			want:    []string{},
		},
		{
			name:    "um volume vira -v nome-estável:path",
			volumes: []VolumeMount{{ID: "vol-abc", MountPath: "/data"}},
			want:    []string{"-v", "adila-vol-abc-data:/data"},
		},
		{
			name: "vários volumes preservam a ordem",
			volumes: []VolumeMount{
				{ID: "vol-1", MountPath: "/var/lib/data"},
				{ID: "vol-2", MountPath: "/cache"},
			},
			want: []string{
				"-v", "adila-vol-1-data:/var/lib/data",
				"-v", "adila-vol-2-data:/cache",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := appVolumeArgs(tc.volumes)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("appVolumeArgs() = %v, quer %v", got, tc.want)
			}
		})
	}
}

// O nome do volume Docker tem de ser estável e independente do container — é o que
// faz os dados sobreviverem aos redeploys (cada deploy recria o container).
func TestVolumeNameIsStable(t *testing.T) {
	if got := volumeName("vol-xyz"); got != "adila-vol-xyz-data" {
		t.Fatalf("volumeName = %q, quer adila-vol-xyz-data", got)
	}
}
