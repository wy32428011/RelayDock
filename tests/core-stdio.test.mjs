import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { createInterface } from 'node:readline';
import { test } from 'node:test';
import { corePath, root } from '../scripts/build-core.mjs';

test('真实 Go 进程通过标准流处理握手、目录、设置与本机样本库', {timeout:25000}, async()=>{
  const data=await mkdtemp(join(tmpdir(),'relaydock-core-smoke-'));
  const child=spawn(corePath(), ['-data',data,'-resources',resolve(root,'resources')],{stdio:['pipe','pipe','pipe'],windowsHide:true});
  const exited=once(child,'exit');
  child.stderr.resume();
  child.stdin.on('error',()=>{});
  const reader=createInterface({input:child.stdout});
  const stream=reader[Symbol.asyncIterator]();
  let serial=0;
  async function frame(method,params={},v=1){
    const id=`test-${++serial}`;
    child.stdin.write(JSON.stringify({v,type:'request',id,method,params})+'\n');
    for(;;){
      const {done,value}=await stream.next(); assert.equal(done,false,'Go 核心提前退出');
      const message=JSON.parse(value);
      if(message.type==='event')continue;
      assert.equal(message.id,id);assert.equal(message.type,'response');return message;
    }
  }
  async function call(method,params){const result=await frame(method,params);assert.equal(result.ok,true,`调用 ${method} 失败：${result.error?.code}`);return result.result;}
  const kill=setTimeout(()=>child.kill(),18000); kill.unref();
  try {
    assert.equal((await frame('host.list')).error.code,'HANDSHAKE_REQUIRED');
    assert.equal((await frame('hello',{supported:[2]},2)).error.code,'VERSION_MISMATCH');
    const hello=await call('hello',{supported:[1]}); assert.equal(hello.selected,1); assert.match(hello.version,/^\d+\.\d+\.\d+/);
    assert.deepEqual(await call('host.list'),[]);
    const catalog=await call('catalog.list'); assert.equal(catalog.length,12); assert.ok(catalog.some(app=>app.id==='mosquitto'));
    assert.deepEqual(await call('resource.list'),[]);
    assert.deepEqual(await call('operation.list',{limit:4}),[]);
    const settings=await call('settings.get'); assert.ok(['dark','light','system'].includes(settings.theme));
    const settingKeys=['theme','connectionTimeoutSeconds','retryCount','retryIntervalSeconds','maxConcurrentHosts','repositoryUrl','updateCheckUrl','proxyUrl','cacheLimitMiB'];
    const allowed=Object.fromEntries(settingKeys.map(k=>[k,settings[k]]));
    const saved=await call('settings.save',{...allowed,theme:'light'}); assert.equal(saved.theme,'light');
    const custom=await call('custom.save', {id:'',name:'合成部署定义',kind:'CUSTOM',description:'本机测试，不运行',directory:'/tmp/demo',frontendDirectory:'',configPath:'',startCommand:'',stopCommand:'',statusCommand:'',logsPath:'',enabled:true});
    assert.ok(custom.id); assert.ok((await call('custom.list')).some(x=>x.id===custom.id));
    assert.equal((await call('custom.delete',{id:custom.id})).ok,true);
    assert.equal((await frame('unknown.experimental')).error.code,'UNKNOWN_METHOD');
    child.stdin.end();
    assert.deepEqual(await exited,[0,null]);
  } finally {
    clearTimeout(kill);
    if(child.exitCode===null && !child.killed){child.kill();await exited.catch(()=>{});}
    reader.close();
    await rm(data,{recursive:true,force:true,maxRetries:3});
  }
});
