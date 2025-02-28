PKG=github.com/BeCrafter/plugo
BINDIR=bin
BINS=plugo
PLUGINS=plugo-hello-world plugo-sleep
PKGDEPS=

all: clean vet fmt build

build: libplugo $(BINS) $(PLUGINS)

fmt:
	go fmt $(PKG)/...

vet:
	go vet $(PKG)/...

libplugo:
	go build $(RACE) $(PKG)

clean:
	rm -rf $(BINDIR)/plugins/*
	rm -rf $(BINDIR)/*

bindir:
	mkdir -p $(BINDIR)

bindirplug:
	mkdir -p $(BINDIR)/plugins

$(BINS): bindir
	go build $(RACE) -o $(BINDIR)/$@ $(PKG)/examples/$@

$(PLUGINS): bindirplug
	go build $(RACE) -o $(BINDIR)/plugins/$@ $(PKG)/examples/$@

$(PKGDEPS):
	go get -u $@

.PHONY: all deps build clean fmt vet $(BINS) $(EXAMPLES) $(PKGDEPS)
