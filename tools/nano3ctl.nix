# Static riscv64 build of nano3ctl.c for the Avalon Nano 3.
# Build: nix build -f tools/nano3ctl.nix -o tools/nano3ctl-riscv64
# nixpkgs is pinned to the immutable rev this workspace was built on (no --impure,
# no dependence on the machine's flake registry).
let
  nixpkgs = builtins.getFlake "github:NixOS/nixpkgs/3ec636362e1816bc2f0888fa0fa2c69f1bc9e504";
  pkgs = nixpkgs.legacyPackages.x86_64-linux.pkgsCross.riscv64;
in
pkgs.stdenv.mkDerivation {
  name = "nano3ctl";
  src = ./.;
  dontConfigure = true;
  buildInputs = [ pkgs.glibc.static ];
  buildPhase = ''
    $CC -static -no-pie -O2 -Wall -o nano3ctl nano3ctl.c
  '';
  installPhase = ''
    mkdir -p $out/bin
    cp nano3ctl $out/bin/
  '';
}
