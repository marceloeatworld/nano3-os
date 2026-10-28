# Static riscv64 strace for the Avalon Nano 3 (glibc static, no libdw/libunwind).
# Build: nix build -f tools/strace-static.nix -o tools/strace-riscv64
# nixpkgs is pinned to the immutable rev this workspace was built on (no --impure,
# no dependence on the machine's flake registry).
let
  nixpkgs = builtins.getFlake "github:NixOS/nixpkgs/3ec636362e1816bc2f0888fa0fa2c69f1bc9e504";
  pkgs = nixpkgs.legacyPackages.x86_64-linux.pkgsCross.riscv64;
in
pkgs.strace.overrideAttrs (old: {
  pname = "strace-static";
  buildInputs = [ pkgs.glibc.static ];
  configureFlags = (old.configureFlags or [ ]) ++ [
    "--enable-mpers=no"
    "--without-libdw"
    "--without-libunwind"
  ];
  # A plain LDFLAGS attribute does not reach the linker; NIX_CFLAGS_LINK does. PIE must be off for -static.
  env = (old.env or { }) // { NIX_CFLAGS_LINK = "-static -no-pie -static-libgcc"; };
  doCheck = false;
})
