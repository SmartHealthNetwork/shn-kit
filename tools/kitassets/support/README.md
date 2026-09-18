# Local validator support

The packaged validator loads `shn.fhir.validation-support-1.2.0.tgz` locally.
The package contains the complete CMS HCPCS and Place of Service terminology used
by PAS validation, and the validation closure of the HL7 cross-version
`Claim.encounter` extension: 25 resources copied unchanged from the pinned
`hl7.fhir.uv.xver-r5.r4` 0.1.0 and `hl7.fhir.uv.extensions.r4` 5.3.0-ballot-tc1
packages, derived by the walk in `closure.py`, so the extension, the R5 Encounter
profile it targets and what they reference resolve on every line without loading
either package whole.

`sources.json` records the official URLs and SHA-256 digests of every input,
including both archives and every copied member. `source.tar.gz` contains the
unchanged downloaded inputs, their notices, the detailed scope README, the
deterministic generator, the closure walk with its committed output, and the
tests. To reproduce, extract the archive into an empty directory and run
`python3 generate.py` followed by `python3 test_generate.py` there; with the two
archives placed in a directory named by `SHN_IG_ARCHIVES`, `python3 closure.py`
re-derives the closure and the tests prove it byte-identical to the archives.
`build-source-tar.py` rebuilds `source.tar.gz` from the repository tree.

CMS's original notices remain in the archived public use files. The HL7 sources
declare CC0-1.0. The package includes CMS Level II codes; it does not include or
synthesize AMA CPT Level I or ADA CDT datasets. Historical code membership does
not establish current billability.
