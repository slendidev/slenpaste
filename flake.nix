{
  description = "A simple pastebin service";

  inputs = {
    nixpkgs.url     = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { nixpkgs, flake-utils, ... }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs { inherit system; };
      in
				{
					packages = {
						default = pkgs.buildGoModule {
							pname          = "slenpaste";
							version        = "0.1.0";
							src            = ./.;
							goPackagePath  = "github.com/slendidev/slenpaste";
							vendorHash     = null;
						};
					};

					devShells.default = pkgs.mkShell {
						buildInputs = [ pkgs.go pkgs.gopls ];
					};

					nixosModules.slenpaste = { lib, pkgs, config, ... }: {
						# module function
						options.services.slenpaste.enable = lib.mkEnableOption "Enable slenpaste service";
						options.services.slenpaste.domain = lib.mkOption {
							type        = lib.types.str;
							default     = "localhost";
							description = "Domain to serve pastes from";
						};
						options.services.slenpaste.listen = lib.mkOption {
							type        = lib.types.str;
							default     = "0.0.0.0:8080";
							description = "Listen address (host:port)";
						};
						options.services.slenpaste.staticDir = lib.mkOption {
							type        = lib.types.str;
							default     = "/var/lib/slenpaste";
							description = "Directory which contains the actual paste data";
						};
						options.services.slenpaste.expireDur = lib.mkOption {
							type        = lib.types.str;
							default     = "0";
							description = "Expiry duration (Go syntax, e.g. \"5m\", \"1h\" or \"0\" for none)";
						};
						options.services.slenpaste.expireOnView = lib.mkOption {
							type        = lib.types.bool;
							default     = false;
							description = "Whether to expire on first view";
						};

						config = lib.mkIf config.services.slenpaste.enable {
							systemd.services.slenpaste = {
								description = "slenpaste HTTP paste service";
								after       = [ "network.target" ];
								wants       = [ "network.target" ];
								serviceConfig = {
									ExecStart = ''
										${pkgs.slenpaste}/bin/slenpaste \
											-domain ${config.services.slenpaste.domain} \
											-listen ${config.services.slenpaste.listen} \
											-expire ${config.services.slenpaste.expireDur} \
											-expire-on-view=${toString config.services.slenpaste.expireOnView}
									'';
									Restart   = "on-failure";
								};
								wantedBy = [ "multi-user.target" ];
							};
						};
					};
				}
    );
}
