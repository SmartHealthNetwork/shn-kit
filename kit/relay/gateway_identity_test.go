package relay

import (
	"bytes"
	"debug/buildinfo"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
)

const (
	publishedBarrierVersion = "v0.44.0"
	publishedBarrierSum     = "h1:Mn4El5H2YvaJCv+i/PfjZldGUXKf+P8YB7+6Oemd1ks="
	publishedLegacyVersion  = "v0.43.1"
	publishedLegacySum      = "h1:XKVtKSaDam/e9KJhB4+lbsJP/3f8ktLZV4qJ1A4Dctw="
)

func buildInfo(version, sum string) *debug.BuildInfo {
	return &debug.BuildInfo{Path: "github.com/SmartHealthNetwork/shn-gateway/cmd/gateway", Main: debug.Module{Path: "github.com/SmartHealthNetwork/shn-gateway", Version: version, Sum: sum}}
}

func TestGatewayProfileExactMetadata(t *testing.T) {
	for _, rel := range []struct {
		version, sum string
		want         GatewayProfile
	}{
		{publishedBarrierVersion, publishedBarrierSum, GatewayBarrier0440},
		{publishedLegacyVersion, publishedLegacySum, GatewayLegacySync0431},
	} {
		t.Run(rel.version, func(t *testing.T) {
			if got := gatewayProfile(buildInfo(rel.version, rel.sum)); got != rel.want {
				t.Fatalf("exact public binary read as %d, want %d", got, rel.want)
			}
			for name, mutate := range map[string]func(*debug.BuildInfo){"devel": func(b *debug.BuildInfo) { b.Main.Version = "(devel)" }, "version": func(b *debug.BuildInfo) { b.Main.Version = "v0.43.2" }, "sum": func(b *debug.BuildInfo) { b.Main.Sum = "" }, "wrong sum": func(b *debug.BuildInfo) { b.Main.Sum = "other" }, "replace": func(b *debug.BuildInfo) { b.Main.Replace = &debug.Module{} }, "package": func(b *debug.BuildInfo) { b.Path = "other" }, "module": func(b *debug.BuildInfo) { b.Main.Path = "other" }} {
				t.Run(name, func(t *testing.T) {
					b := buildInfo(rel.version, rel.sum)
					mutate(b)
					if gatewayProfile(b) != GatewayUnknown {
						t.Fatal("mutated metadata trusted")
					}
				})
			}
		})
	}
	// One release's version carrying another's checksum identifies neither: the
	// pair is the identity, so a recognized half can never stand in for both.
	for _, mixed := range []struct{ version, sum string }{
		{publishedBarrierVersion, publishedLegacySum},
		{publishedLegacyVersion, publishedBarrierSum},
	} {
		t.Run("crossed "+mixed.version, func(t *testing.T) {
			if got := gatewayProfile(buildInfo(mixed.version, mixed.sum)); got != GatewayUnknown {
				t.Fatalf("crossed version and checksum read as %d", got)
			}
		})
	}
	if gatewayProfile(nil) != GatewayUnknown {
		t.Fatal("nil metadata trusted")
	}
}

// The gateway executable this Kit packages must read as a named release.
// Packaging refuses an unnamed one, so a pin the reader cannot identify would
// leave the Kit unpackageable rather than merely undiagnosed — and a pin moved
// without its profile would hand the new release the older one's concessions.
func TestPackagedGatewayPinIsRecognized(t *testing.T) {
	version, sum := pinnedGateway(t)
	if ExpectedGatewayProfile == GatewayUnknown {
		t.Fatal("the packaged gateway release has no profile of its own")
	}
	if got := gatewayProfile(buildInfo(version, sum)); got != ExpectedGatewayProfile {
		t.Fatalf("module pin %s %s reads as %d, want the packaged profile %d", version, sum, got, ExpectedGatewayProfile)
	}
	if got := publishedReleases[0].profile; got != ExpectedGatewayProfile {
		t.Fatalf("first recognized release has profile %d, want the packaged profile %d", got, ExpectedGatewayProfile)
	}
}

// pinnedGateway reads the gateway release this module requires, and its
// checksum, from the module files themselves rather than from a second copy of
// the same numbers.
func pinnedGateway(t *testing.T) (version, sum string) {
	t.Helper()
	const module = "github.com/SmartHealthNetwork/shn-gateway"
	read := func(name string) []string {
		b, err := os.ReadFile(filepath.Join("..", name))
		if err != nil {
			t.Fatal(err)
		}
		return strings.Split(string(b), "\n")
	}
	for _, line := range read("go.mod") {
		if f := strings.Fields(line); len(f) == 2 && f[0] == module {
			version = f[1]
		}
	}
	if version == "" {
		t.Fatalf("the module file requires no %s", module)
	}
	for _, line := range read("go.sum") {
		if f := strings.Fields(line); len(f) == 3 && f[0] == module && f[1] == version {
			sum = f[2]
		}
	}
	if sum == "" {
		t.Fatalf("no checksum recorded for %s %s", module, version)
	}
	return version, sum
}

func TestAgreedProfileNeedsEveryArchitectureToAgree(t *testing.T) {
	for _, row := range []struct {
		name   string
		slices []GatewayProfile
		want   GatewayProfile
	}{
		{"no architecture", nil, GatewayUnknown},
		{"one recognized", []GatewayProfile{GatewayBarrier0440}, GatewayBarrier0440},
		{"agreeing", []GatewayProfile{GatewayBarrier0440, GatewayBarrier0440}, GatewayBarrier0440},
		{"agreeing on the earlier release", []GatewayProfile{GatewayLegacySync0431, GatewayLegacySync0431}, GatewayLegacySync0431},
		{"two different releases", []GatewayProfile{GatewayBarrier0440, GatewayLegacySync0431}, GatewayUnknown},
		{"recognized then unidentified", []GatewayProfile{GatewayBarrier0440, GatewayUnknown}, GatewayUnknown},
		{"unidentified then recognized", []GatewayProfile{GatewayUnknown, GatewayBarrier0440}, GatewayUnknown},
		// A later architecture matching the first must not undo the disagreement
		// in between.
		{"disagreement between matches", []GatewayProfile{GatewayBarrier0440, GatewayLegacySync0431, GatewayBarrier0440}, GatewayUnknown},
	} {
		t.Run(row.name, func(t *testing.T) {
			if got := agreedProfile(row.slices); got != row.want {
				t.Fatalf("architectures %v read as %d, want %d", row.slices, got, row.want)
			}
		})
	}
}
func TestInspectGatewayBinary(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	os.WriteFile(src, []byte("package main\nfunc main(){}"), 0600)
	// A thin executable's container format must not be mistaken for malformed
	// fat Mach-O. Every supported platform is checked on every test host.
	for _, target := range []struct {
		goos, goarch string
		magic        []byte
	}{
		{"windows", "amd64", []byte("MZ")},
		{"linux", "amd64", []byte("\x7fELF")},
		{"darwin", "arm64", []byte{0xcf, 0xfa, 0xed, 0xfe}},
	} {
		t.Run(target.goos, func(t *testing.T) {
			binary := filepath.Join(dir, "child-"+target.goos)
			cmd := exec.Command("go", "build", "-o", binary, src)
			cmd.Env = append(os.Environ(), "GOWORK=off", "GOOS="+target.goos, "GOARCH="+target.goarch, "CGO_ENABLED=0")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("build: %s: %v", out, err)
			}
			data, err := os.ReadFile(binary)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.HasPrefix(data, target.magic) {
				t.Fatalf("wrong container magic: %x", data[:4])
			}
			if _, err := buildinfo.ReadFile(binary); err != nil {
				t.Fatalf("baseline metadata unreadable: %v", err)
			}
			if p, err := InspectGatewayBinary(binary); err != nil || p != GatewayUnknown {
				t.Fatalf("development thin executable: %v %v", p, err)
			}
			// Recognizing the container alone must never excuse missing Go metadata.
			broken := bytes.Replace(data, []byte("\xff Go buildinf:"), []byte("x Go buildinf:"), 1)
			if bytes.Equal(broken, data) {
				t.Fatal("fixture has no build metadata to corrupt")
			}
			if err := os.WriteFile(binary, broken, 0600); err != nil {
				t.Fatal(err)
			}
			if p, err := InspectGatewayBinary(binary); err == nil || p != GatewayUnknown {
				t.Fatalf("corrupt thin metadata accepted: %v %v", p, err)
			}
		})
	}
	for name, data := range map[string][]byte{"malformed": []byte("bad"), "empty fat": {0xca, 0xfe, 0xba, 0xbe, 0, 0, 0, 0}} {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(dir, name)
			os.WriteFile(p, data, 0600)
			if got, err := InspectGatewayBinary(p); err == nil || got != GatewayUnknown {
				t.Fatalf("invalid binary: %v %v", got, err)
			}
		})
	}
}

func TestInspectEveryFatSlice(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	os.WriteFile(src, []byte("package main\nfunc main(){}"), 0600)
	var slices [][]byte
	for _, arch := range []string{"amd64", "arm64"} {
		binary := filepath.Join(dir, arch)
		cmd := exec.Command("go", "build", "-o", binary, src)
		cmd.Env = append(os.Environ(), "GOWORK=off", "GOOS=darwin", "GOARCH="+arch, "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build: %s: %v", out, err)
		}
		b, err := os.ReadFile(binary)
		if err != nil {
			t.Fatal(err)
		}
		slices = append(slices, b)
	}
	makeFat := func(second []byte) string {
		const off1 = 4096
		off2 := (off1 + len(slices[0]) + 4095) &^ 4095
		b := make([]byte, off2+len(second))
		binary.BigEndian.PutUint32(b, 0xcafebabe)
		binary.BigEndian.PutUint32(b[4:], 2)
		for i, v := range []struct{ cpu, sub, off, size uint32 }{{0x1000007, 3, off1, uint32(len(slices[0]))}, {0x100000c, 0, uint32(off2), uint32(len(second))}} {
			o := 8 + 20*i
			for j, n := range []uint32{v.cpu, v.sub, v.off, v.size, 12} {
				binary.BigEndian.PutUint32(b[o+j*4:], n)
			}
		}
		copy(b[off1:], slices[0])
		copy(b[off2:], second)
		p := filepath.Join(dir, "universal")
		os.WriteFile(p, b, 0600)
		return p
	}
	if p, err := InspectGatewayBinary(makeFat(slices[1])); p != GatewayUnknown || err != nil {
		t.Fatalf("devel universal: %v %v", p, err)
	}
	broken := bytes.Replace(slices[1], []byte("\xff Go buildinf:"), []byte("x Go buildinf:"), 1)
	if p, err := InspectGatewayBinary(makeFat(broken)); p != GatewayUnknown || err == nil {
		t.Fatalf("second architecture metadata was not inspected: %v %v", p, err)
	}
}
