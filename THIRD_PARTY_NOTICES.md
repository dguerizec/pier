# Third-Party Notices

Pier is distributed under the MIT License. Its release archives also include
third-party license notices for the Go modules linked into the `pier` binary.

The notices live under `third_party_licenses/` and were generated with:

```sh
go-licenses save ./cmd/pier --save_path=third_party_licenses
```

`modernc.org/mathutil` is included manually because `go-licenses` reports it
as unknown even though the module ships BSD-3-Clause license files.

The current dependency set is permissive: MIT, BSD-3-Clause, and Apache-2.0.
