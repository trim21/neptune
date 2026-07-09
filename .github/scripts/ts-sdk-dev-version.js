// @ts-check
import {execSync} from 'node:child_process';
import {readFileSync} from 'node:fs';
import {resolve} from 'node:path';

const workspace = process.env.GITHUB_WORKSPACE ?? process.cwd();
const pkg = JSON.parse(readFileSync(resolve(workspace, 'sdk/typescript/package.json'), 'utf-8'));

let tag;
try {
  tag = execSync('git describe --tags --match "sdk/typescript/v*" --abbrev=0', {
    encoding: 'utf-8',
    stdio: ['pipe', 'pipe', 'ignore'],
  }).trim();
} catch {
  tag = '';
}

const range = tag ? `${tag}..HEAD` : 'HEAD';
const distance = execSync(`git rev-list --count ${range}`, {encoding: 'utf-8'}).trim();

console.log(distance === '0' ? pkg.version : `${pkg.version}-dev.${distance}`);
