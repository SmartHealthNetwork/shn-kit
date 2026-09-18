package relay

import (
	"debug/buildinfo"
	"debug/macho"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"runtime/debug"
)

// ExpectedGatewayProfile is the provenance the gateway executable packaged with
// this Kit carries: the published release this Kit's module file pins. Packaging
// reads a candidate executable and refuses anything else, so a Kit never ships
// a gateway child whose identity it cannot name.
const ExpectedGatewayProfile = GatewayBarrier0440

// publishedRelease pairs one published gateway release's module version with its
// checksum. Both halves must match: a version alone names bytes nobody verified.
type publishedRelease struct {
	version, sum string
	profile      GatewayProfile
}

// publishedReleases are the gateway releases this Kit recognizes. The first is
// the release it packages (ExpectedGatewayProfile); the others are earlier
// releases a Kit may still be pointed at, kept so their observation keeps
// working rather than silently degrading.
var publishedReleases = []publishedRelease{
	{"v0.44.0", "h1:Mn4El5H2YvaJCv+i/PfjZldGUXKf+P8YB7+6Oemd1ks=", GatewayBarrier0440},
	{"v0.43.1", "h1:XKVtKSaDam/e9KJhB4+lbsJP/3f8ktLZV4qJ1A4Dctw=", GatewayLegacySync0431},
}

func gatewayProfile(b *debug.BuildInfo) GatewayProfile {
	if b == nil || b.Path != "github.com/SmartHealthNetwork/shn-gateway/cmd/gateway" || b.Main.Path != "github.com/SmartHealthNetwork/shn-gateway" || b.Main.Replace != nil {
		return GatewayUnknown
	}
	for _, rel := range publishedReleases {
		if b.Main.Version == rel.version && b.Main.Sum == rel.sum {
			return rel.profile
		}
	}
	return GatewayUnknown
}

// agreedProfile reduces one executable's per-architecture provenance. A
// universal executable is a published release only when every architecture in
// it reads as that same release; architectures that disagree name no release,
// however many of them match.
func agreedProfile(slices []GatewayProfile) GatewayProfile {
	if len(slices) == 0 {
		return GatewayUnknown
	}
	for _, p := range slices[1:] {
		if p != slices[0] {
			return GatewayUnknown
		}
	}
	return slices[0]
}

// InspectGatewayBinary reads build provenance, including every fat Mach-O slice.
// Unknown provenance degrades diagnostics only; it never gates child admission.
func InspectGatewayBinary(path string) (GatewayProfile, error) {
	f, err := os.Open(path)
	if err != nil {
		return GatewayUnknown, err
	}
	defer f.Close()
	// NewFatFile's ErrNotFat identifies thin Mach-O only, not PE or ELF.
	// Detect universal containers first so malformed fat files cannot fall
	// through to buildinfo.Read, which inspects only their first architecture.
	var header [4]byte
	if _, err := f.ReadAt(header[:], 0); err != nil {
		return GatewayUnknown, err
	}
	magic := binary.BigEndian.Uint32(header[:])
	if magic == macho.MagicFat || magic == 0xcafebabf { // fat32 or fat64 signature
		fat, err := macho.NewFatFile(f)
		if err != nil {
			return GatewayUnknown, err
		}
		if len(fat.Arches) == 0 {
			return GatewayUnknown, errors.New("empty fat executable")
		}
		// Every architecture is read, even once one has already disagreed, so
		// unreadable metadata anywhere in the container is still an error.
		slices := make([]GatewayProfile, 0, len(fat.Arches))
		for _, a := range fat.Arches {
			info, err := buildinfo.Read(io.NewSectionReader(f, int64(a.Offset), int64(a.Size)))
			if err != nil {
				return GatewayUnknown, err
			}
			slices = append(slices, gatewayProfile(info))
		}
		return agreedProfile(slices), nil
	}
	info, err := buildinfo.Read(f)
	if err != nil {
		return GatewayUnknown, err
	}
	return gatewayProfile(info), nil
}
