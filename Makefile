.PHONY:all proto

proto:
	cd eraftpb && go generate; cd -

all : proto
	@echo "pb complied"