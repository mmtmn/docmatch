# Fórmula do Homebrew para o docmatch 0.0.0-dfc5c5c819b6, gerada pelo tools/installer/release.sh a partir
# do SHA256SUMS.txt da versão (molde em tools/installer/packaging/templates/docmatch.rb). Os
# binários são os mesmos do install.sh, nas cópias fixas da versão.
class Docmatch < Formula
  desc "Instalador e linha de comando da docmatch: concilia documentos nesta máquina"
  homepage "https://docmatch.com.br"
  version "0.0.0-dfc5c5c819b6"
  license "AGPL-3.0-only"

  on_macos do
    on_arm do
      url "https://docmatch.com.br/install/v0.0.0-dfc5c5c819b6/docmatch-darwin-arm64.bin"
      sha256 "593dc19544a0c69e8870f6df0ceceb6b9a670966c99f6d4f7ee683ea61a55a99"
    end
    on_intel do
      url "https://docmatch.com.br/install/v0.0.0-dfc5c5c819b6/docmatch-darwin-amd64.bin"
      sha256 "e6980ad24274581aa71ca266e60bbb650f55caa1c7b99c7d583c1a0ceb0faa48"
    end
  end

  on_linux do
    on_arm do
      url "https://docmatch.com.br/install/v0.0.0-dfc5c5c819b6/docmatch-linux-arm64.bin"
      sha256 "44bd2c657e1066f692b9c8850deb3f74fabb5b670e4fe5d790f5f6701484510e"
    end
    on_intel do
      url "https://docmatch.com.br/install/v0.0.0-dfc5c5c819b6/docmatch-linux-amd64.bin"
      sha256 "948f511a2b2467be5620dfe7160afbdc8af620803213e466451ea1f93c2ec11f"
    end
  end

  def install
    bin.install Dir["docmatch-*.bin"].first => "docmatch"
  end

  test do
    assert_equal "docmatch #{version}", shell_output("#{bin}/docmatch --version").strip
  end
end
