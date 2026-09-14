const vm=require('node:vm');
const fs=require('node:fs');
const assert=require('node:assert/strict');
const code=fs.readFileSync('static/push.js','utf8');
function fixture({permission='default',rejectSave=false,expired=false,existing=false,revoked=false,brave=false,subscribeError=''}={}){
 const elements=Object.fromEntries(['push-status','push-last-result','push-enable','push-disable','push-test'].map(k=>[k,{}]));let permissionCalled=false,saves=0,unsubscribed=false;
 const sub={endpoint:'https://push.example/current',options:{},toJSON(){return{endpoint:this.endpoint,keys:{auth:'x',p256dh:'y'}}},async unsubscribe(){unsubscribed=true;return true}};
 const reg={scope:'https://app.example/',active:{},pushManager:{async getSubscription(){return existing?sub:null},async subscribe(){if(subscribeError)throw new DOMException(subscribeError,'AbortError');return sub}}};
 const context=vm.createContext({console,URL,Promise,Uint8Array,AbortController,setTimeout,clearTimeout,atob,location:{origin:'https://app.example'},isSecureContext:true,
  window:{PushManager:{},Notification:{}},navigator:{brave:brave?{async isBrave(){return true}}:undefined,serviceWorker:{async register(){return reg},async getRegistrations(){return[reg]}}},DOMException,
  Notification:{permission,requestPermission(){permissionCalled=true;return Promise.resolve('granted')}},
  document:{getElementById(id){return elements[id]},addEventListener(){}},
  async fetch(path,options){let data={key:'AQID'},ok=true;if(path==='/push/subscribe'){saves++;if(revoked){data={error:'Subscription expired. Enable this device again.',renew:true};ok=false}else if(rejectSave){data={error:'Registration rejected'};ok=false}else data={registered:true}}if(path==='/push/test')data={accepted:!expired,registered:!expired,message:expired?'Subscription expired':'Accepted by push service'};return{status:ok?200:500,ok,redirected:false,async json(){return data}};}
 });vm.runInContext(code,context);return{context,elements,permissionCalled:()=>permissionCalled,saves:()=>saves,unsubscribed:()=>unsubscribed};
}
(async()=>{
 const good=fixture();await vm.runInContext('preparePush()',good.context);vm.runInContext('enablePush()',good.context);assert.equal(good.permissionCalled(),true,'permission must be requested before returning from tap handler');await new Promise(r=>setTimeout(r,0));assert.equal(vm.runInContext('pushRegistered',good.context),true);await vm.runInContext('testPush()',good.context);assert.match(good.elements['push-last-result'].textContent,/Accepted by push service/);
 const failed=fixture({rejectSave:true});await vm.runInContext('preparePush()',failed.context);vm.runInContext('enablePush()',failed.context);await new Promise(r=>setTimeout(r,0));assert.equal(vm.runInContext('pushRegistered',failed.context),false);assert.match(failed.elements['push-status'].textContent,/Registration rejected/);
 const denied=fixture({permission:'denied'});await vm.runInContext('preparePush()',denied.context);assert.match(denied.elements['push-status'].textContent,/Permission blocked/);assert.equal(denied.permissionCalled(),false);
 const stale=fixture({expired:true});await vm.runInContext('preparePush()',stale.context);vm.runInContext('enablePush()',stale.context);await new Promise(r=>setTimeout(r,0));await vm.runInContext('testPush()',stale.context);assert.equal(vm.runInContext('pushRegistered',stale.context),false);assert.equal(stale.elements['push-enable'].hidden,false);vm.runInContext('enablePush()',stale.context);await new Promise(r=>setTimeout(r,0));assert.equal(stale.unsubscribed(),true,'expired subscription must be replaced');
 const revoked=fixture({existing:true,revoked:true,permission:'granted'});await vm.runInContext('preparePush()',revoked.context);assert.equal(vm.runInContext('pushNeedsRenewal',revoked.context),true);assert.equal(revoked.elements['push-enable'].hidden,false);
 const braveFailure=fixture({brave:true,subscribeError:'Registration failed - push service error'});await vm.runInContext('preparePush()',braveFailure.context);vm.runInContext('enablePush()',braveFailure.context);await new Promise(r=>setTimeout(r,0));assert.match(braveFailure.elements['push-status'].textContent,/brave:\/\/settings\/privacy/i);
 console.log('Push smoke passed: direct user activation, failed registration, denied permission, device test, expired subscription renewal.');
})().catch(e=>{console.error(e);process.exit(1)});
