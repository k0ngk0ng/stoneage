import copy
import json
import tempfile
import time
import unittest
from pathlib import Path

from arena_agent.learning import Learned, examples, features, train
from arena_agent.storage import Store
from arena_agent.strategies import Basic
from arena_agent.contract import ContractError, validate_plan
from test_core import fixture


class LearningTests(unittest.IsolatedAsyncioTestCase):
    async def test_real_training_interface_and_joint_inference(self):
        with tempfile.TemporaryDirectory() as temp:
            store = Store(Path(temp)/'data')
            for n in range(12):
                team = fixture(2)
                team['match_id'] = f'match-{n}'
                for v in team['members'].values():
                    v['battle']['Participants'] = [
                        {'BattleID':0,'HP':100,'MaxHP':100},
                        {'BattleID':10,'HP':10 if n%2 else 100,'MaxHP':100}]
                plan = (await Basic().decide(team,[],time.monotonic()+1)).plan
                store.record('turn',{'team':team,'plan':plan},team['match_id'])
                for o in plan['orders']:
                    selection = {'match_id':team['match_id'],'turn':team['turn'],
                                 'observation_id':'fixture','candidate_id':o['candidate_id']}
                    store.reserve(o['member_id'],selection,o['actor'])
                    store.finish_intent(o['member_id'],selection,o['actor'],{'ok':True})
                store.result({'id':team['match_id'],'winner_side':n%2,'reason':'defeat',
                              'members':[{'id':f'roster-{n//2}'}]})
            database = str(store.directory/'arena.sqlite3')
            store.close()
            result = train([database],Path(temp)/'model.json')
            self.assertIsNotNone(result['training']['held_out'])
            model = Learned(Path(temp)/'model.json',2)
            decision = await model.decide(team,[],time.monotonic()+2)
            validate_plan(team,decision.plan)
            self.assertEqual(len(decision.plan['orders']),4)
            changed = copy.deepcopy(team)
            changed['rules_version'] = 'different-server-rules'
            with self.assertRaises(ContractError):
                await model.decide(changed,[],time.monotonic()+1)
            with self.assertRaises(ValueError): Learned(Path(temp)/'model.json',1)
            rows,_ = examples([database])
            self.assertEqual(len(rows),12)
            with self.assertRaises(FileExistsError): train([database],Path(temp)/'model.json')

    async def test_features_exclude_hidden_data_and_include_joint_focus(self):
        team = fixture(2)
        plan = (await Basic().decide(team,[],time.monotonic()+1)).plan
        choices = {(o['member_id'],o['actor']):o['candidate_id'] for o in plan['orders']}
        first = features(team,choices)
        altered = copy.deepcopy(team)
        altered['future_result'] = {'winner_side':1}
        altered['opponent_private_attack'] = 99999
        self.assertEqual(features(altered,choices),first)
        choices[('member-1','player')] = 'attack-11'
        self.assertLess(features(team,choices)['focus:enemy'],first['focus:enemy'])


if __name__ == '__main__': unittest.main()
