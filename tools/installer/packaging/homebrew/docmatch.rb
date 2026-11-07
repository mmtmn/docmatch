# Fórmula do Homebrew para o docmatch 0.2.0, gerada pelo tools/installer/release.sh a partir
# do SHA256SUMS.txt da versão (molde em tools/installer/packaging/templates/docmatch.rb). Os
# binários são os mesmos do install.sh, nas cópias fixas da versão.
class Docmatch < Formula
  desc "Instalador e linha de comando da docmatch: concilia documentos nesta máquina"
  homepage "https://docmatch.com.br"
  version "0.2.0"
  license "AGPL-3.0-only"

  on_macos do
    on_arm do
      url "https://docmatch.com.br/install/v0.2.0/docmatch-darwin-arm64.bin"
      sha256 "ea03d5d31f454be0a359c0c06ead7cb14373e4c305a72e5750cfaacbf595b95e"
    end
    on_intel do
      url "https://docmatch.com.br/install/v0.2.0/docmatch-darwin-amd64.bin"
      sha256 "490a7b653f202db2a257b3e8ae30c749a8f5cd521057877fa87335ed4186c828"
    end
  end

  on_linux do
    on_arm do
      url "https://docmatch.com.br/install/v0.2.0/docmatch-linux-arm64.bin"
      sha256 "8c686b8725c6d3590b9d85a1cabd5036d75fb6cfe3093d8bd450075f84fe0aa9"
    end
    on_intel do
      url "https://docmatch.com.br/install/v0.2.0/docmatch-linux-amd64.bin"
      sha256 "39e323947559abcf545d8bd4d2d2a22f66cb9d289a03f61dbd28ca117a6c3849"
    end
  end

  def install
    bin.install Dir["docmatch-*.bin"].first => "docmatch"
  end

  test do
    assert_equal "docmatch #{version}", shell_output("#{bin}/docmatch --version").strip
  end
end
