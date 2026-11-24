import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, beforeAll, expect, it, vi } from 'vitest';
import type { AppEntry, Bridge, Host } from '@relaydock/protocol';
import { HostDialog } from '../apps/desktop/src/renderer/hosts';
import { Catalog } from '../apps/desktop/src/renderer/overview';

const host: Host = {id:'host-1',name:'合成远端',address:'test.invalid',port:22,username:'tester',group:'验证',auth:'password',os:'ubuntu',osVersion:'24.04',arch:'amd64',fingerprint:'SHA256:fixture',credentialReady:true};
const redis: AppEntry = {id:'redis',label:'Redis',description:'键值存储',category:'cache',versions:['7.2.3'],params:[],platforms:['ubuntu'],supportsOnline:false};

beforeAll(()=>{
  if(!HTMLDialogElement.prototype.showModal) Object.defineProperty(HTMLDialogElement.prototype,'showModal',{configurable:true,value:function(this:HTMLDialogElement){this.setAttribute('open','');}});
  if(!HTMLDialogElement.prototype.close) Object.defineProperty(HTMLDialogElement.prototype,'close',{configurable:true,value:function(this:HTMLDialogElement){this.removeAttribute('open');}});
});
afterEach(cleanup);

it('首次添加必须明确核对指纹，取得测试令牌之后才能保存', async()=>{
  const onSaved=vi.fn();
  const call=vi.fn(async (method:string,params:Record<string,unknown>)=>{
    if(method==='host.probe') return params.acceptFingerprint ? {state:'ready',probeId:'checked',host,sftp:false} : {state:'trust',fingerprint:host.fingerprint,changed:false};
    if(method==='host.save') return host;
    throw new Error('Unexpected method');
  });
  const api={call:call as unknown as Bridge['call'],subscribe:()=>()=>{}};
  render(<HostDialog api={api} available onCancel={()=>{}} onSaved={onSaved}/>);
  for(const [name,value] of [['主机名称',host.name],['服务器地址',host.address],['登录用户名',host.username],['连接密码','synthetic-only']]) {
    fireEvent.change(screen.getByLabelText(name),{target:{value}});
  }
  expect(screen.queryByRole('button',{name:'保存主机'})).toBeNull();
  fireEvent.click(screen.getByRole('button',{name:'测试连接'}));
  const trust=await screen.findByRole<HTMLButtonElement>('button',{name:'信任指纹并测试'});
  expect(trust.disabled).toBe(true);
  fireEvent.click(screen.getByRole('checkbox',{name:'已通过独立渠道核对此指纹'}));
  fireEvent.click(trust);
  expect(await screen.findByText('SFTP 不可用')).toBeTruthy();
  fireEvent.click(await screen.findByRole('button',{name:'保存主机'}));
  expect(call).toHaveBeenLastCalledWith('host.save',{probeId:'checked'});
  await vi.waitFor(()=>expect(onSaved).toHaveBeenCalledWith(host));
});

it('未选主机时目录可浏览，但不发起安装或卸载',()=>{
  const launch=vi.fn();
  render(<Catalog catalog={[redis]} ready loading={false} tasks={[]} error="" onOperate={launch} onResources={()=>{}}/>);
  const action=screen.getByRole<HTMLButtonElement>('button',{name:'安装 Redis'});
  expect(action.disabled).toBe(true);
  fireEvent.click(action);
  expect(launch).not.toHaveBeenCalled();
});
