// A gallery of the card types and a few diagram sketches, with illustrative data only: a preview sheet, not a real status.
import React from 'react';
import Metric from './metric-card';
import Breakdown from './breakdown-card';
import Rings from './rings-card';
import List from './list-card';
import Tasks from './tasks-card';
const colors=['#67a6ff','#b69aff','#56d9b0'];
function Diagram({architecture=false}:{architecture?:boolean}) {
 const node=(t:string)=><div style={{display:'flex',alignItems:'center',justifyContent:'center',background:'#213047',border:'1px solid #405574',borderRadius:10,width:110,height:55,fontSize:15}}>{t}</div>;
 return <div style={{display:'flex',alignItems:'center',justifyContent:'space-between',marginTop:70}}>{node(architecture?'Web app':'Request')}<span style={{color:'#67a6ff'}}>→</span>{node(architecture?'API':'Validate')}<span style={{color:'#67a6ff'}}>→</span>{architecture?<div style={{display:'flex',flexDirection:'column',gap:24}}>{node('Database')}{node('Worker')}</div>:node('Deliver')}</div>;
}
export default function Gallery(){
 const box:React.CSSProperties={display:'flex',flexDirection:'column',width:513,flexShrink:0,background:'#141c29',border:'1px solid #2b374b',borderRadius:16,padding:16,overflow:'hidden',height:370};
 const label=(s:string)=><div style={{display:'flex',flexDirection:'column',fontSize:15,color:'#8ea5c7',marginBottom:12}}>{s}</div>;
 return <div style={{display:'flex',flexDirection:'column',background:'#090f19',color:'#eef4ff',fontFamily:'Inter, sans-serif',padding:28,width:1100,boxSizing:'border-box'}}><div style={{display:'flex',flexDirection:'column',fontSize:30,fontWeight:700}}>JARVIS · Visual examples</div><div style={{display:'flex',flexDirection:'column',color:'#9caec8',margin:'8px 0 24px'}}>Illustrative data only — not live status or actual architecture</div><div style={{display:'flex',flexDirection:'row',flexWrap:'wrap',gap:18}}>
 <div style={box}>{label('1 · Metric + line chart')}<Metric title="Success rate" value="84%" delta="" caption="Example weekly trend" series={[71,80,64,90,88,75,84]} labels={['Tue','Wed','Thu','Fri','Sat','Sun','Mon']}/></div>
 <div style={box}>{label('2 · Donut breakdown')}<Breakdown title="Budget split" subtitle="Example allocation" center="100" centerSub="units" unit=" units" segments={[{label:'Compute',value:50},{label:'AI models',value:30},{label:'Storage',value:20}]}/></div>
 <div style={box}>{label('3 · Goal rings')}<Rings title="Goal progress" subtitle="Example targets" rings={[{label:'Tests passed',current:34,goal:40},{label:'Tasks done',current:6,goal:8},{label:'Messages handled',current:18,goal:20}]}/></div>
 <div style={box}>{label('4 · Agent status card')}<List title="Example agents" subtitle="Illustrative statuses" items={[{title:'Frontend',detail:'Checking layout',status:'busy',value:'working'},{title:'Backend',detail:'Needs a decision',status:'warn',value:'waiting'},{title:'QA',detail:'Checks finished',status:'ok',value:'done'}]}/></div>
 <div style={box}>{label('5 · Task progress bars')}<Tasks title="Example tasks" subtitle="Illustrative progress" tasks={[{id:1,title:'Dashboard',status:'in_progress',progress:75,note:'Browser checks remaining'},{id:2,title:'Data import',status:'blocked',progress:40,note:'Waiting for sample file'},{id:3,title:'Documentation',status:'done',progress:100,note:'Reviewed'}]}/></div>
 <div style={box}>{label('6 · Comparison bar chart')}<div style={{display:'flex',flexDirection:'column',fontSize:22,fontWeight:700}}>Response time by service</div><div style={{display:'flex',flexDirection:'column',color:'#9caec8',marginTop:8}}>Example milliseconds · lower is better</div>{[{n:'Service A',v:120,w:40},{n:'Service B',v:240,w:80},{n:'Service C',v:300,w:100}].map((r,i)=><div key={r.n} style={{display:'flex',flexDirection:'column',marginTop:30}}><div style={{display:'flex',justifyContent:'space-between',marginBottom:8}}><span>{r.n}</span><span>{r.v} ms</span></div><div style={{display:'flex',flexDirection:'column',background:'#263246',height:20,borderRadius:6}}><div style={{display:'flex',flexDirection:'column',background:colors[i],height:20,width:r.w+'%',borderRadius:6}}/></div></div>)}</div>
 <div style={box}>{label('7 · Architecture diagram')}<div style={{display:'flex',flexDirection:'column',fontSize:22,fontWeight:700}}>Example system components</div><Diagram architecture/></div>
 <div style={box}>{label('8 · Workflow diagram')}<div style={{display:'flex',flexDirection:'column',fontSize:22,fontWeight:700}}>Example request journey</div><Diagram/></div>
 </div></div>;
}
Gallery.PreviewProps={};
Gallery.PreviewSize={width:1100,height:1790};
