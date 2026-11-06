class Kq < Formula
  desc "Relationship-aware Kubernetes viewer: kubectl get, plus everything it references or owns"
  homepage "https://github.com/Fanatic-zer0/kq"
  version "0.1.0"
  license "Apache-2.0"

  # Update url/sha256 per release; goreleaser publishes kq_<os>_<arch>.tar.gz archives.
  if OS.mac? && Hardware::CPU.arm?
    url "https://github.com/Fanatic-zer0/kq/releases/download/v0.1.0/kq_darwin_arm64.tar.gz"
    sha256 "REPLACE_WITH_RELEASE_SHA256"
  elsif OS.mac?
    url "https://github.com/Fanatic-zer0/kq/releases/download/v0.1.0/kq_darwin_amd64.tar.gz"
    sha256 "REPLACE_WITH_RELEASE_SHA256"
  elsif OS.linux?
    url "https://github.com/Fanatic-zer0/kq/releases/download/v0.1.0/kq_linux_amd64.tar.gz"
    sha256 "REPLACE_WITH_RELEASE_SHA256"
  end

  def install
    bin.install "kq"
  end

  test do
    system "#{bin}/kq", "--help"
  end
end
