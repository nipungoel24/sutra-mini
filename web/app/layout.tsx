import type { Metadata } from 'next'; import './styles.css';
export const metadata: Metadata={title:'Sutra Mini | Sync',description:'Reliable job-work voucher sync dashboard',robots:{index:false,follow:false}};
export default function RootLayout({children}:{children:React.ReactNode}){return <html lang="en"><body>{children}</body></html>}
