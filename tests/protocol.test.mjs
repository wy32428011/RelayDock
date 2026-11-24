import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {test} from 'node:test';
import Ajv2020 from 'ajv/dist/2020.js';

const schema=JSON.parse(readFileSync(new URL('../packages/protocol/envelope.schema.json',import.meta.url),'utf8'));
const validate=new Ajv2020().compile(schema);
const fixtures=readFileSync(new URL('../testdata/protocol.jsonl',import.meta.url),'utf8').trim().split(/\r?\n/u).map(line=>JSON.parse(line));

test('共享 v1 请求、响应、进度与终端事件符合 JSON Schema',()=>{
  fixtures.forEach(frame=>assert.equal(validate(frame),true,JSON.stringify(validate.errors)));
});
test('拒绝无效版本、空关联 ID、伪造复合响应和负序号',()=>{
  const [hello,,,progress]=fixtures;
  for(const frame of [{...hello,v:2},{...hello,id:''},{...hello,params:[]},{...progress,seq:-1},
    {v:1,type:'response',id:'r1',ok:true,result:{},error:{code:'X',message:'x',retryable:true}},
    {v:1,type:'response',id:'r1',ok:true}]) assert.equal(validate(frame),false);
});
