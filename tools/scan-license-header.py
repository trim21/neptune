import glob
import sys
import re
import posixpath

pattern = re.compile(r"// SPDX-License-Identifier: (.*)\n")


def main():
    ret = 0
    for file in glob.iglob("**/*.go", recursive=True):
        with open(file, "r") as f:
            lines = f.readlines()

        file = file.replace("\\", "/")

        matches = []
        for lino, line in enumerate(lines):
            lino = lino + 1
            m = pattern.findall(line)
            if m:
                matches.append((lino, m[0]))

        if len(matches) == 0:
            print(f"{file}:1", "missing license header")
            ret = 1
            continue

        if len(matches) > 1:
            for lino, m in matches:
                print(f"{file}:{lino}", "found multiple license headers:", m)
            ret = 1
            continue

        lino, m = matches[0]

        if m not in ["GPL-3.0-only", "MIT", "MPL-2.0", "Apache-2.0"]:
            print(f"{file}:{lino}", "invalid license:", m)
            ret = 1
            continue

    sys.exit(ret)


if __name__ == "__main__":
    main()
