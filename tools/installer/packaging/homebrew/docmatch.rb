# Fórmula do Homebrew para o docmatch 0.0.0-20c41fbebb49, gerada pelo tools/installer/release.sh a partir
# do SHA256SUMS.txt da versão (molde em tools/installer/packaging/templates/docmatch.rb). Os
# binários são os mesmos do install.sh, nas cópias fixas da versão.
class Docmatch < Formula
  desc "Instalador e linha de comando da docmatch: concilia documentos nesta máquina"
  homepage "https://docmatch.com.br"
  version "0.0.0-20c41fbebb49"
  license "AGPL-3.0-only"

  on_macos do
    on_arm do
      url "https://docmatch.com.br/install/v0.0.0-20c41fbebb49/docmatch-darwin-arm64.bin"
      sha256 "83095fa16eca27cdff78a953d5dde902b3ba7ab01f5b522ca213c644c50dfac5"
    end
    on_intel do
      url "https://docmatch.com.br/install/v0.0.0-20c41fbebb49/docmatch-darwin-amd64.bin"
      sha256 "62de387edfc849d3ed7f3eabe9da6c2c99f20419b86fa418ee8792d47ab014d6"
    end
  end

  on_linux do
    on_arm do
      url "https://docmatch.com.br/install/v0.0.0-20c41fbebb49/docmatch-linux-arm64.bin"
      sha256 "4584307910f4f259671c3e0154c8cbf0840684636e20a292c35480b32d2b8893"
    end
    on_intel do
      url "https://docmatch.com.br/install/v0.0.0-20c41fbebb49/docmatch-linux-amd64.bin"
      sha256 "9b34cbef6bb010abfe462c20e7d371d274862f2c71ca97f8b546d2bc9a553904"
    end
  end

  def install
    bin.install Dir["docmatch-*.bin"].first => "docmatch"
  end

  test do
    assert_equal "docmatch #{version}", shell_output("#{bin}/docmatch --version").strip
  end
end
