import { createHash } from 'node:crypto';
import { createReadStream, existsSync, statSync, writeFileSync } from 'node:fs';
import { basename, resolve } from 'node:path';
import pkg from 'electron-builder';
import { buildApp } from './build-app.mjs';
import { buildCore, metadata, root } from './build-core.mjs';

const { build, Platform, Arch } = pkg;
const args = process.argv.slice(2);
const dirOnly = args.includes('--dir');
const ix = args.indexOf('--arch');
const arch = ix >= 0 ? args[ix+1] : process.arch;
if (args.some((arg,i)=>!['--dir','--arch'].includes(arg) && !(ix>=0 && i===ix+1)) || !['x64','arm64'].includes(arch)) {
  throw new Error('用法：npm run package:local -- [--dir] [--arch x64|arm64]');
}
const choices = {win32:[Platform.WINDOWS,['nsis']], darwin:[Platform.MAC,['dmg']], linux:[Platform.LINUX,['deb','AppImage']]};
if (!Object.hasOwn(choices,process.platform)) throw new Error(`不支持的构建平台 ${process.platform}`);
const platform = process.platform;
const [builderPlatform, targetNames] = choices[platform];
const core = buildCore(platform, arch);
await buildApp();
const output = resolve(root,'artifacts',`${platform}-${arch}`);
const localElectron = resolve(root,'node_modules','electron','dist');
const coreEntry = {from:core,to:`bin/${basename(core)}`};
const signingDisabled = platform==='win32' ? {signExecutable:false} : {};
process.env.CSC_IDENTITY_AUTO_DISCOVERY = 'false';
for (const env of ['CSC_LINK','CSC_KEY_PASSWORD','WIN_CSC_LINK','WIN_CSC_KEY_PASSWORD','APPLE_ID','APPLE_APP_SPECIFIC_PASSWORD','APPLE_API_KEY','APPLE_API_KEY_ID','APPLE_API_ISSUER','APPLE_TEAM_ID']) delete process.env[env];
const builtFiles = await build({
  projectDir:root, publish:'never',
  targets:builderPlatform.createTarget(dirOnly ? ['dir'] : targetNames, Arch[arch]),
  config:{
    appId:'com.relaydock.desktop', productName:'RelayDock', asar:true, npmRebuild:false,
    electronVersion:metadata.devDependencies.electron,
    ...(arch===process.arch && existsSync(localElectron) ? {electronDist:localElectron} : {}),
    directories:{output,buildResources:'packaging'},
    files:['apps/desktop/dist/**/*','LICENSE','package.json','!**/node_modules/**'],
    extraResources:[
      {from:'resources',to:'resources',filter:['catalog.json','scripts/**/*.sh','scripts/**/*.ps1']},
      ...(platform==='darwin' ? [] : [coreEntry]),
    ],
    artifactName:'RelayDock-${version}-${os}-${arch}.${ext}',
    win:{icon:'packaging/icon.ico',...signingDisabled},
    nsis:{oneClick:false, perMachine:false, allowToChangeInstallationDirectory:true, deleteAppDataOnUninstall:false,
      installerIcon:'packaging/icon.ico',uninstallerIcon:'packaging/icon.ico',shortcutName:'RelayDock',
      // Disabling multiLanguageInstaller forces en_US even with a Chinese PE version resource.
      unicode:true,language:'2052',installerLanguages:['zh_CN'],displayLanguageSelector:false},
    mac:{identity:null, category:'public.app-category.developer-tools',icon:'packaging/icon.png',notarize:false,
      extraFiles:[{from:core,to:`Helpers/${basename(core)}`}]},
    dmg:{title:'${productName} ${version} 安装'},
    linux:{category:'Utility',icon:'packaging/icon.png',synopsis:'远程安装工作台',maintainer:'RelayDock contributors <maintainers@relaydock.invalid>'},
  },
});
const artifacts = [];
const installers = dirOnly ? [] : [...new Set(builtFiles)].filter(file => /\.(?:exe|dmg|deb|AppImage)$/i.test(file)).sort();
for (const file of installers) {
  const hash = createHash('sha256');
  for await (const chunk of createReadStream(file)) hash.update(chunk);
  artifacts.push({ name: basename(file), bytes: statSync(file).size, sha256: hash.digest('hex') });
}
if (!dirOnly && artifacts.length === 0) throw new Error('未收到本次生成的安装包路径');
writeFileSync(resolve(output,'BUILD.json'), JSON.stringify({version:metadata.version,platform,arch,mode:dirOnly?'unpacked':'installer',signed:false,artifacts},null,2)+'\n');
console.log(`本地产物（未发布）：${output}`);
