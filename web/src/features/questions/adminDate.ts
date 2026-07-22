export function canonicalRFC3339(value:Date):string{
  const iso=value.toISOString()
  return iso.replace(/\.(\d{3})Z$/,(_match,fraction:string)=>{const canonical=fraction.replace(/0+$/,'');return canonical?`.${canonical}Z`:'Z'})
}

export function adminDateBoundary(value:string,boundary:'from'|'to',now=new Date()):string|undefined{
  if(!/^\d{4}-\d{2}-\d{2}$/.test(value))return undefined
  const [year,month,day]=value.split('-').map(Number)
  const candidate=boundary==='from'?new Date(year,month-1,day):new Date(year,month-1,day+1,0,0,0,-1)
  if(candidate.getFullYear()!==year||candidate.getMonth()!==month-1||candidate.getDate()!==day)return undefined
  const bounded=boundary==='to'&&candidate.getTime()>now.getTime()?now:candidate
  return canonicalRFC3339(bounded)
}
