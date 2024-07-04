import glob
import sys


def main():
    ret = 0
    for file in glob.iglob("**/*.go", recursive=True):
        with open(file, "r") as f:
            lines = f.read()
            if "// SPDX-License-Identifier: " not in lines:
                print("missing license header:", file)
                ret = 1
    sys.exit(ret)


if __name__ == "__main__":
    main()
