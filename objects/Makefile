PYTHON = ../.venv/bin/python
FIX_MESH = ../scripts/fix_mesh.py

SRCS = $(wildcard *.glb)
CLEAN = $(filter-out %-clean.glb,$(SRCS))
TARGETS = $(CLEAN:.glb=-clean.glb)

all: $(TARGETS)

%-clean.glb: %.glb $(FIX_MESH)
	$(PYTHON) $(FIX_MESH) $< $@

clean:
	rm -f *-clean.glb

.PHONY: all clean
